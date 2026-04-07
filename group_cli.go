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

func parseGroupFlagValue(raw string) (groupActionTarget, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return groupActionTarget{}, false
	}
	return groupActionTarget{group: value, via: "group", original: value}, true
}

func resolveGroupActionTarget(raw string) (groupActionTarget, error) {
	if virtual, ok := parseVirtualTarget(raw); ok {
		if virtual.kind == "group" {
			return groupActionTarget{group: virtual.name, via: "group", original: raw}, nil
		}
		resolved, err := propertyResolveTarget(raw)
		if err != nil {
			return groupActionTarget{}, err
		}
		return groupActionTarget{group: resolved.Group, via: "target", original: raw}, nil
	}
	unitName := normalizeGroupScopedUnitName(raw)
	if unitName == "" {
		return groupActionTarget{}, fmt.Errorf("group target is required")
	}
	resp, ok := queryUnitGroups(unitName)
	if !ok {
		return groupActionTarget{}, fmt.Errorf("could not resolve groups for %s", unitName)
	}
	if len(resp.Groups) == 0 {
		return groupActionTarget{}, fmt.Errorf("%s does not belong to a group", unitName)
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

func shouldAutoResolveGroupAction(action string) bool {
	switch action {
	case "enable", "disable", "is-enabled", "status":
		return true
	default:
		return false
	}
}

func maybeResolveGroupActionTarget(action string, raw string) (groupActionTarget, bool, error) {
	if !shouldAutoResolveGroupAction(action) {
		return groupActionTarget{}, false, nil
	}
	if _, ok := parseVirtualTarget(raw); ok {
		target, err := resolveGroupActionTarget(raw)
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

func groupMembershipLabel(resp visionapi.UnitGroupsResponse) string {
	groups := make([]string, 0, len(resp.Groups))
	for _, group := range resp.Groups {
		groups = append(groups, group.Name)
	}
	return strings.Join(uniqueSortedStrings(groups), ", ")
}
