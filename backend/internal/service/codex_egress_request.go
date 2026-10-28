package service

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	codexEnvironmentTimezone = regexp.MustCompile(`<timezone>[^<>\r\n]*</timezone>`)
	codexEnvironmentDate     = regexp.MustCompile(`<current_date>[^<>\r\n]*</current_date>`)
)

// Align explicit request metadata and Codex's generated environment context with
// the selected account's egress. Patch only matching text fields so unrelated
// input items, tool arguments and large JSON numbers retain their wire values.
func rewriteCodexEgressTimezone(body []byte, account *Account, now time.Time) ([]byte, error) {
	if account == nil || !account.IsOpenAIOAuth() || account.codexEgressProfile == nil || account.codexEgressProfile.timezone == "" || !gjson.ValidBytes(body) {
		return body, nil
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body, nil
	}
	zone := account.codexEgressProfile.timezone
	location, err := time.LoadLocation(zone)
	if err != nil {
		return body, nil
	}
	if root.Get("timezone").Exists() {
		body, err = sjson.SetBytes(body, "timezone", zone)
		if err != nil {
			return nil, err
		}
	}
	if root.Get("timezone_offset_min").Exists() {
		_, offset := now.In(location).Zone()
		// JavaScript getTimezoneOffset: minutes west of UTC, including DST.
		body, err = sjson.SetBytes(body, "timezone_offset_min", -offset/60)
		if err != nil {
			return nil, err
		}
	}
	type textPatch struct {
		path, text string
	}
	var patches []textPatch
	collectText := func(value gjson.Result, path, role string) {
		if value.Type != gjson.String {
			return
		}
		text := value.String()
		tag := "environment_context"
		if role == "developer" {
			tag = "codex_apps_client_time_context"
		}
		if next := rewriteCodexTimeContext(text, tag, zone, now.In(location).Format("2006-01-02")); next != text {
			patches = append(patches, textPatch{path, next})
		}
	}
	collectMessage := func(item gjson.Result, path string) {
		role := item.Get("role").String()
		if !item.IsObject() || (role != "user" && role != "developer") {
			return
		}
		if kind := item.Get("type").String(); kind != "" && kind != "message" {
			return
		}
		content := item.Get("content")
		if content.Type == gjson.String {
			collectText(content, path+".content", role)
		} else if content.IsArray() {
			for i, part := range content.Array() {
				if kind := part.Get("type").String(); kind == "input_text" || kind == "text" {
					collectText(part.Get("text"), path+".content."+strconv.Itoa(i)+".text", role)
				}
			}
		}
	}
	input := root.Get("input")
	switch {
	case input.Type == gjson.String:
		collectText(input, "input", "user")
	case input.IsArray():
		for i, item := range input.Array() {
			collectMessage(item, "input."+strconv.Itoa(i))
		}
	case input.IsObject():
		collectMessage(input, "input")
	}
	for _, patch := range patches {
		body, err = sjson.SetBytes(body, patch.path, patch.text)
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

func rewriteCodexEnvironmentTime(text, zone, date string) string {
	return rewriteCodexTimeContext(text, "environment_context", zone, date)
}

func rewriteCodexTimeContext(text, tag, zone, date string) string {
	// Codex sends the generated context as its own text part, sometimes alongside
	// AGENTS.md in the same message. Do not rewrite quoted examples or prose that
	// merely mentions the XML tags, nor interpret the rest of the prompt as XML
	// (cwd and other environment values are not necessarily XML-escaped).
	// 0.165 additionally emits a standalone developer time context after
	// compaction. It must agree with both the user environment and attestation.
	open, close := "<"+tag+">", "</"+tag+">"
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, open) || !strings.HasSuffix(trimmed, close) ||
		strings.Count(trimmed, open) != 1 || strings.Count(trimmed, close) != 1 ||
		!codexEnvironmentTimezone.MatchString(trimmed) {
		return text
	}
	text = codexEnvironmentTimezone.ReplaceAllString(text, "<timezone>"+zone+"</timezone>")
	// Keep the date consistent when the egress is on the other side of midnight.
	// Missing fields stay absent; there is no synthetic environment block.
	return codexEnvironmentDate.ReplaceAllString(text, "<current_date>"+date+"</current_date>")
}
