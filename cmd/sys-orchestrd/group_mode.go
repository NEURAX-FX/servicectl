package main

import (
	"fmt"
	"strings"

	"servicectl/internal/visionapi"
)

func (d *daemon) isGroupMode() bool {
	return strings.TrimSpace(d.group) != ""
}

func (d *daemon) objectName() string {
	if d.isGroupMode() {
		return "group:" + d.group
	}
	return d.unit
}

func (d *daemon) initialGroupSync() error {
	state, ok := d.queryGroup(d.group)
	if !ok {
		return fmt.Errorf("group %s not found", d.group)
	}
	d.groupUnits = state.Units
	if !state.Enabled {
		d.writeState("waiting", "group-disabled")
		d.publishState("waiting", "group-disabled")
		return nil
	}
	d.writeState("starting", "group-enabled")
	d.publishState("starting", "group-enabled")
	if err := d.runGroupAction("start", d.groupUnits); err != nil {
		d.writeState("failed", "group-start-error")
		d.publishState("failed", "group-start-error")
		return err
	}
	d.writeState("running", "group-enabled")
	d.publishState("running", "group-enabled")
	return nil
}

func (d *daemon) handleGroupScopedChange(event visionapi.EventEnvelope) error {
	group := strings.TrimSpace(event.Payload["group"])
	if group == "" {
		return nil
	}
	if d.isGroupMode() {
		if group != d.group {
			return nil
		}
		state, ok := d.queryGroup(d.group)
		if !ok {
			return nil
		}
		d.groupUnits = state.Units
		enabled := strings.EqualFold(strings.TrimSpace(event.Payload["enabled"]), "yes") || strings.TrimSpace(event.Payload["enabled"]) == "1"
		if enabled {
			d.writeState("starting", "group-enabled:"+group)
			d.publishState("starting", "group-enabled:"+group)
			if err := d.runGroupAction("start", d.groupUnits); err != nil {
				d.writeState("failed", "group-start-error:"+group)
				d.publishState("failed", "group-start-error:"+group)
				return err
			}
			d.writeState("running", "group-enabled:"+group)
			d.publishState("running", "group-enabled:"+group)
			return nil
		}
		d.writeState("stopping", "group-disabled:"+group)
		d.publishState("stopping", "group-disabled:"+group)
		if err := d.runGroupAction("stop", reverseServiceOrder(d.groupUnits)); err != nil {
			return err
		}
		d.writeState("waiting", "group-disabled:"+group)
		d.publishState("waiting", "group-disabled:"+group)
		return nil
	}
	if !d.groups[group] {
		return nil
	}
	return d.handleGroupChange(event)
}

func reverseServiceOrder(units []string) []string {
	result := make([]string, 0, len(units))
	for i := len(units) - 1; i >= 0; i-- {
		result = append(result, units[i])
	}
	return result
}
