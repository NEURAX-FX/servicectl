package main

var externalManagedStateFunc = isExternallyManaged

func isEffectivelyEnabled(unitName string) bool {
	return effectiveEnabledFromFlags(isEnabledWithS6(unitName), externalManagedStateFunc(unitName))
}

func managementState(unitName string) string {
	return managementStateFromFlags(isEnabledWithS6(unitName), externalManagedStateFunc(unitName))
}

func effectiveEnabledFromFlags(managed bool, external bool) bool {
	return managed || external
}

func managementStateFromFlags(managed bool, external bool) string {
	switch {
	case managed && external:
		return "internal+external"
	case external:
		return "external"
	case managed:
		return "internal"
	default:
		return "unmanaged"
	}
}
