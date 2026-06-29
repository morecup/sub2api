package handler

import "strings"

func forwardErrorFallbackMessage(err error) string {
	if err == nil {
		return "upstream error"
	}
	message := strings.TrimSpace(err.Error())
	for _, prefix := range []string{
		"upstream request failed:",
		"Upstream request failed:",
	} {
		if strings.HasPrefix(message, prefix) {
			message = strings.TrimSpace(strings.TrimPrefix(message, prefix))
			break
		}
	}
	if message == "" {
		return "upstream error"
	}
	return message
}
