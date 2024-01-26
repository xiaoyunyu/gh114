package utils

import "strings"

func SafeSplit(s, sep string) []string {
	fields := strings.Split(s, sep)
	filtered := []string{}
	for _, f := range fields {
		if f == "" {
			continue
		}
		filtered = append(filtered, f)
	}
	return filtered
}
