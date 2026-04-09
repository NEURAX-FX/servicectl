package main

import "fmt"

func externalManageUnit(unitName string, enabled bool) int {
	if _, err := parseSystemdUnit(unitName); err != nil {
		fmt.Println(err)
		return 1
	}
	if err := setExternallyManaged(unitName, enabled); err != nil {
		fmt.Println(err)
		return 1
	}
	if enabled {
		fmt.Printf("Marked external-managed: %s\n", unitName)
	} else {
		fmt.Printf("Cleared external-managed: %s\n", unitName)
	}
	return 0
}
