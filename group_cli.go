package main

import (
	"fmt"
	"strings"

	"servicectl/internal/visionapi"
)

type groupActionTarget struct {
	group    string
	via      string
	original string
}

func explicitGroupExists(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	_, ok := queryGroupState(strings.TrimSpace(name))
	return ok
}

func isGroupAction(action string) bool {
	switch strings.TrimSpace(action) {
	case "enable", "disable", "is-enabled", "status":
		return true
	default:
		return false
	}
}

func normalizeGroupScopedUnitName(raw string) string {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return ""
	}
	if !strings.HasSuffix(clean, ".service") {
		clean += ".service"
	}
	return clean
}

func resolveGroupSelector(raw string) (groupActionTarget, error) {
	selector := strings.TrimSpace(raw)
	if selector == "" {
		return groupActionTarget{}, fmt.Errorf("group selector is required")
	}
	if virtual, ok := parseVirtualTarget(selector); ok {
		if virtual.kind == "group" {
			if !explicitGroupExists(virtual.name) {
				return groupActionTarget{}, fmt.Errorf("group %s not found", virtual.name)
			}
			return groupActionTarget{group: virtual.name, via: "group", original: raw}, nil
		}
		resolved, err := propertyResolveTarget(selector)
		if err != nil {
			return groupActionTarget{}, err
		}
		return groupActionTarget{group: resolved.Group, via: "target", original: raw}, nil
	}
	if explicitGroupExists(selector) {
		return groupActionTarget{group: selector, via: "group", original: raw}, nil
	}
	if resolved, err := propertyResolveTarget(selector + ".target"); err == nil && strings.TrimSpace(resolved.Group) != "" {
		return groupActionTarget{group: resolved.Group, via: "target", original: selector + ".target"}, nil
	}
	unitName := normalizeGroupScopedUnitName(selector)
	resp, ok := queryUnitGroups(unitName)
	if !ok {
		return groupActionTarget{}, fmt.Errorf("could not resolve group selector %s", selector)
	}
	if len(resp.Groups) == 0 {
		return groupActionTarget{}, fmt.Errorf("could not resolve group selector %s", selector)
	}
	if len(resp.Groups) > 1 {
		groups := make([]string, 0, len(resp.Groups))
		for _, group := range resp.Groups {
			groups = append(groups, group.Name)
		}
		return groupActionTarget{}, fmt.Errorf("%s belongs to multiple groups: %s", unitName, strings.Join(uniqueSortedStrings(groups), ", "))
	}
	return groupActionTarget{group: resp.Groups[0].Name, via: "unit", original: raw}, nil
}

func shouldAutoResolveGroupAction(action string, raw string) bool {
	switch strings.TrimSpace(action) {
	case "enable", "disable", "is-enabled":
		selector := strings.TrimSpace(raw)
		if selector == "" {
			return false
		}
		if _, ok := parseVirtualTarget(selector); ok {
			return true
		}
		return strings.HasSuffix(selector, ".target")
	default:
		return false
	}
}

func maybeResolveGroupActionTarget(action string, raw string) (groupActionTarget, bool, error) {
	if !shouldAutoResolveGroupAction(action, raw) {
		return groupActionTarget{}, false, nil
	}
	if _, ok := parseVirtualTarget(raw); ok {
		target, err := resolveGroupSelector(raw)
		return target, true, err
	}
	unitName := normalizeGroupScopedUnitName(raw)
	resp, ok := queryUnitGroups(unitName)
	if !ok || len(resp.Groups) == 0 {
		return groupActionTarget{}, false, nil
	}
	if len(resp.Groups) > 1 {
		groups := make([]string, 0, len(resp.Groups))
		for _, group := range resp.Groups {
			groups = append(groups, group.Name)
		}
		return groupActionTarget{}, true, fmt.Errorf("%s belongs to multiple groups: %s", unitName, strings.Join(uniqueSortedStrings(groups), ", "))
	}
	return groupActionTarget{group: resp.Groups[0].Name, via: "unit", original: raw}, true, nil
}

func handleGroupAction(action string, target groupActionTarget) (int, bool) {
	state, ok := queryGroupState(target.group)
	if action == "status" && !ok {
		fmt.Printf("Group %s not found\n", target.group)
		return 1, true
	}
	switch action {
	case "enable":
		if err := propertySet("persist.group."+target.group, "1", true); err != nil {
			fmt.Println(oneLineError("enable group", err))
			return 1, true
		}
		if err := enableGroupWithS6(target.group); err != nil {
			fmt.Println(oneLineError("enable group with s6", err))
			return 1, true
		}
		fmt.Printf("Enabled group:%s\n", target.group)
		return 0, true
	case "disable":
		if err := propertySet("persist.group."+target.group, "0", true); err != nil {
			fmt.Println(oneLineError("disable group", err))
			return 1, true
		}
		if err := disableGroupWithS6(target.group); err != nil {
			fmt.Println(oneLineError("disable group with s6", err))
			return 1, true
		}
		for _, unit := range state.Units {
			_ = stopUnit(strings.TrimSuffix(unit, ".service"))
		}
		fmt.Printf("Disabled group:%s\n", target.group)
		return 0, true
	case "is-enabled":
		if ok && state.Enabled && isGroupEnabledWithS6(target.group) {
			fmt.Println("enabled")
			return 0, true
		}
		fmt.Println("disabled")
		return 1, true
	case "status":
		status := "disabled"
		if state.Enabled {
			status = "enabled"
		}
		fmt.Printf("group:%s %s\n", target.group, status)
		fmt.Printf("Units: %s\n", strings.Join(state.Units, ", "))
		fmt.Printf("Targets: %s\n", strings.Join(state.Targets, ", "))
		fmt.Printf("S6 Service: %s\n", s6GroupOrchestrdServiceName(target.group))
		return 0, true
	}
	return 0, false
}

func parseGroupInvocation(args []string, currentAction string, currentTargets []string, currentGroup string) (string, []string, string, bool, error) {
	if strings.TrimSpace(currentGroup) == "" {
		return currentAction, currentTargets, currentGroup, false, nil
	}
	if isGroupAction(currentAction) {
		return currentAction, currentTargets, currentGroup, true, nil
	}
	if isGroupAction(currentGroup) {
		if strings.TrimSpace(currentAction) == "" {
			return currentAction, currentTargets, currentGroup, true, fmt.Errorf("group name is required")
		}
		return currentGroup, currentTargets, currentAction, true, nil
	}
	if len(args) >= 2 && isGroupAction(args[0]) {
		return args[0], args[2:], args[1], true, nil
	}
	return currentAction, currentTargets, currentGroup, true, fmt.Errorf("unknown group action %q", currentAction)
}

func resolveExplicitGroupInvocation(action string, selector string) (groupActionTarget, error) {
	target, err := resolveGroupSelector(selector)
	if err != nil {
		return groupActionTarget{}, err
	}
	if !isGroupAction(action) {
		return groupActionTarget{}, fmt.Errorf("unknown group action %q", action)
	}
	return target, nil
}

func groupMembershipLabel(resp visionapi.UnitGroupsResponse) string {
	groups := make([]string, 0, len(resp.Groups))
	for _, group := range resp.Groups {
		groups = append(groups, group.Name)
	}
	return strings.Join(uniqueSortedStrings(groups), ", ")
}
