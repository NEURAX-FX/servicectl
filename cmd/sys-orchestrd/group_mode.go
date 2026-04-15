package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"time"

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
		d.groupUnits = nil
		d.writeState("waiting", "group-not-found:"+d.group)
		d.publishState("waiting", "group-not-found:"+d.group)
		return nil
	}
	d.groupUnits = state.Units
	if !state.Enabled {
		d.writeState("waiting", "group-disabled")
		d.publishState("waiting", "group-disabled")
		return nil
	}
	d.writeState("starting", "group-enabled")
	d.publishState("starting", "group-enabled")
	ordered := d.startOrderForGroupUnits(d.groupUnits)
	if err := d.runGroupAction("start", ordered); err != nil {
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
		if group == d.group {
			state, ok := d.queryGroup(d.group)
			if !ok {
				d.groupUnits = nil
				d.writeState("waiting", "group-not-found:"+d.group)
				d.publishState("waiting", "group-not-found:"+d.group)
				return nil
			}
			d.groupUnits = state.Units
		}
		if group != d.group {
			return nil
		}
		enabled := strings.EqualFold(strings.TrimSpace(event.Payload["enabled"]), "yes") || strings.TrimSpace(event.Payload["enabled"]) == "1"
		if enabled {
			d.writeState("starting", "group-enabled:"+group)
			d.publishState("starting", "group-enabled:"+group)
			ordered := d.startOrderForGroupUnits(d.groupUnits)
			if err := d.runGroupAction("start", ordered); err != nil {
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

func (d *daemon) maintainMissingGroup(ctxDone <-chan struct{}) {
	if !d.isGroupMode() {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctxDone:
			return
		case <-ticker.C:
			if len(d.groupUnits) > 0 {
				continue
			}
			_ = d.initialGroupSync()
		}
	}
}

func reverseServiceOrder(units []string) []string {
	result := make([]string, 0, len(units))
	for i := len(units) - 1; i >= 0; i-- {
		result = append(result, units[i])
	}
	return result
}

func (d *daemon) startOrderForGroupUnits(fallback []string) []string {
	bin := os.Getenv("SERVICECTL_BIN")
	if strings.TrimSpace(bin) == "" {
		bin = "servicectl"
	}
	args := []string{"group-start-order", d.group}
	if d.userMode {
		args = append([]string{"--user"}, args...)
	}
	cmd := exec.Command(bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fallback
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasSuffix(line, ".service") {
			line += ".service"
		}
		result = append(result, line)
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}
