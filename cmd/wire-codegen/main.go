// Command wire-codegen generates TypeScript interfaces, decoders, and an SSE
// registry from Go wire types using reflect. Output replaces hand-written
// validators in static-src/validators.ts.
//
// Run: go run ./cmd/wire-codegen   (from apps/vibekit/web/)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/auth"
	"github.com/cplieger/vibekit/internal/forges"
)

const (
	tsUnknown      = "unknown"
	tsIdentityCast = "(v) => v as unknown"
	enumCancelled  = "cancelled"
	enumDelete     = "delete"
	enumPending    = "pending"
	typeMessage    = "Message"
)

// EnumDef defines a named string enum with its valid values.
type EnumDef struct{ Values []string }

// Enums maps Go type names to their enum values.
var Enums = map[string]EnumDef{
	"Role":              {Values: []string{"user", "assistant", "event"}},
	"EventKind":         {Values: []string{"interrupted", enumCancelled, "model_switched", "compacted", "crew", "agent_switched", "compaction_failed", "inbox"}},
	"ToolKind":          {Values: []string{"execute", "shell", "read", "search", "fetch", "edit", "think", "hook", "write", enumDelete, "move", "command", "browser", "switch_mode", "mcp", "other"}},
	"ToolStatus":        {Values: []string{enumPending, "in_progress", "completed", "failed"}},
	"PlanStatus":        {Values: []string{enumPending, "in_progress", "completed"}},
	"CrewStatus":        {Values: []string{"working", "terminated", "error", enumPending}},
	"PendingChangeKind": {Values: []string{"create", "edit", enumDelete}},
	"StopReason":        {Values: []string{"end_turn", enumCancelled, "interrupted"}},
	"ErrorCode":         {Values: []string{"recovery_failed", "bridge_start_failed", "prompt_failed", "agent_not_found", "model_not_found", "agent_config_error", "rate_limit", "stream_timeout", "spawn_failed", "switch_failed", "compaction_failed"}},
	"ForgeKind":         {Values: []string{"github", "gitlab", "codeberg", "gitea"}},
	"Kind":              {Values: []string{"github", "gitlab", "codeberg", "gitea"}}, // forges.Kind = ForgeKind
	"Transport":         {Values: []string{"stdio", "http"}},
	"PendingAction":     {Values: []string{"accept", "reject"}},
	"ClearReason":       {Values: []string{"turn_ended", enumCancelled, "mode_disabled", "chat_deleted", "shutdown", "user_cleared"}},
}

// enumTSName maps Go enum type names to their TS type name (for dedup/aliasing).
var enumTSName = map[string]string{
	"Kind": "ForgeKind", // forges.Kind → ForgeKind in TS
}

// WireTypes is the set of Go struct types to generate TS for.
var WireTypes = []reflect.Type{
	reflect.TypeFor[api.ToolLocation](),
	reflect.TypeFor[api.ToolDiff](),
	reflect.TypeFor[api.ToolCall](),
	reflect.TypeFor[api.PlanEntry](),
	reflect.TypeFor[api.Block](),
	reflect.TypeFor[api.CrewSubagent](),
	reflect.TypeFor[api.CrewPendingStage](),
	reflect.TypeFor[api.Crew](),
	reflect.TypeFor[api.Message](),
	reflect.TypeFor[api.MeteringItem](),
	reflect.TypeFor[api.Usage](),
	reflect.TypeFor[api.SessionMode](),
	reflect.TypeFor[api.SessionModel](),
	reflect.TypeFor[api.ChatHeader](),
	reflect.TypeFor[api.PermissionOption](),
	reflect.TypeFor[api.PendingChange](),
	reflect.TypeFor[api.FileChange](),
	reflect.TypeFor[api.ConnectedPayload](),
	reflect.TypeFor[api.MessageChunkPayload](),
	reflect.TypeFor[api.TurnEndedPayload](),
	reflect.TypeFor[api.PermissionNeededPayload](),
	reflect.TypeFor[api.ErrorPayload](),
	reflect.TypeFor[api.MCPConnectedPayload](),
	reflect.TypeFor[api.MCPOAuthPayload](),
	reflect.TypeFor[api.MCPFailedPayload](),
	reflect.TypeFor[api.MCPDisconnectedPayload](),
	reflect.TypeFor[api.PendingChangeAddedPayload](),
	reflect.TypeFor[api.PendingChangeResolvedPayload](),
	reflect.TypeFor[api.PendingChangesClearedPayload](),
	reflect.TypeFor[api.ChatDeletedPayload](),
	reflect.TypeFor[api.ToolCallPayload](),
	reflect.TypeFor[api.ToolCallUpdatePayload](),
	reflect.TypeFor[api.CommandsUpdatedPayload](),
	reflect.TypeFor[api.AvailableCommand](),
	reflect.TypeFor[forges.ConfiguredForge](),
	reflect.TypeFor[forges.Repo](),
	reflect.TypeFor[forges.PR](),
	reflect.TypeFor[forges.Issue](),
	reflect.TypeFor[forges.Check](),
	reflect.TypeFor[forges.Release](),
	reflect.TypeFor[forges.Label](),
	reflect.TypeFor[forges.User](),
	reflect.TypeFor[forges.DeviceFlowResponse](),
	reflect.TypeFor[forges.PollResult](),
	reflect.TypeFor[auth.WhoamiResponse](),
}

// SSERegEntry maps an SSE event type to a registered struct name.
type SSERegEntry struct {
	EventType string
	TypeName  string
}

// SSEEvents is the set of SSE events to register decoders for.
var SSEEvents = []SSERegEntry{
	{EventType: "chat_created", TypeName: "ChatHeader"},
	{EventType: "chat_deleted", TypeName: "ChatDeletedPayload"},
	{EventType: "chat_updated", TypeName: "ChatHeader"},
	{EventType: "commands_updated", TypeName: "CommandsUpdatedPayload"},
	{EventType: "connected", TypeName: "ConnectedPayload"},
	{EventType: "error", TypeName: "ErrorPayload"},
	{EventType: "mcp_connected", TypeName: "MCPConnectedPayload"},
	{EventType: "mcp_disconnected", TypeName: "MCPDisconnectedPayload"},
	{EventType: "mcp_failed", TypeName: "MCPFailedPayload"},
	{EventType: "mcp_oauth_needed", TypeName: "MCPOAuthPayload"},
	{EventType: "message_appended", TypeName: typeMessage},
	{EventType: "message_chunk", TypeName: "MessageChunkPayload"},
	{EventType: "message_created", TypeName: typeMessage},
	{EventType: "message_updated", TypeName: typeMessage},
	{EventType: "pending_change_added", TypeName: "PendingChangeAddedPayload"},
	{EventType: "pending_change_resolved", TypeName: "PendingChangeResolvedPayload"},
	{EventType: "pending_changes_cleared", TypeName: "PendingChangesClearedPayload"},
	{EventType: "permission_needed", TypeName: "PermissionNeededPayload"},
	{EventType: "tool_call", TypeName: "ToolCallPayload"},
	{EventType: "tool_call_update", TypeName: "ToolCallUpdatePayload"},
	{EventType: "turn_ended", TypeName: "TurnEndedPayload"},
}

// typeByName indexes registered types by Go name for cross-references.
var typeByName = map[string]reflect.Type{}

// tsNameOverride maps Go type names to preferred TS names.
var tsNameOverride = map[string]string{
	"Entry": "RepoEntry",
}

func tsName(goName string) string {
	if override, ok := tsNameOverride[goName]; ok {
		return override
	}
	return goName
}

func init() {
	for _, t := range WireTypes {
		typeByName[t.Name()] = t
	}
}

// fieldInfo holds parsed metadata for one struct field.
type fieldInfo struct {
	goType   reflect.Type
	wireName string
	optional bool
}

func parseFields(t reflect.Type) []fieldInfo {
	var fields []fieldInfo
	for sf := range t.Fields() {
		if sf.Anonymous {
			// Flatten embedded struct fields.
			embedded := sf.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			fields = append(fields, parseFields(embedded)...)
			continue
		}
		tag := sf.Tag.Get("json")
		if tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		wireName := parts[0]
		if wireName == "" {
			wireName = sf.Name
		}
		if wireName == "-" {
			continue
		}
		omitempty := false
		for _, p := range parts[1:] {
			if p == "omitempty" {
				omitempty = true
			}
		}
		optional := omitempty || sf.Type.Kind() == reflect.Pointer
		fields = append(fields, fieldInfo{wireName: wireName, goType: sf.Type, optional: optional})
	}
	return fields
}

// tsType returns the TypeScript type string for a Go reflect.Type.
func tsType(t reflect.Type) string {
	// Unwrap pointer.
	if t.Kind() == reflect.Pointer {
		return tsType(t.Elem())
	}
	// Check named types for enum membership.
	if t.Name() != "" {
		if _, ok := Enums[t.Name()]; ok {
			return tsEnumName(t.Name())
		}
		if _, ok := typeByName[t.Name()]; ok {
			return tsName(t.Name())
		}
	}
	// json.RawMessage → unknown
	if t == reflect.TypeFor[json.RawMessage]() {
		return tsUnknown
	}
	// time.Time → string
	if t == reflect.TypeFor[time.Time]() {
		return "string"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice:
		return tsType(t.Elem()) + "[]"
	case reflect.Map:
		return "Record<string, " + tsType(t.Elem()) + ">"
	case reflect.Interface:
		return tsUnknown
	case reflect.Struct:
		// Anonymous struct — shouldn't happen for registered types.
		return tsUnknown
	}
	return tsUnknown
}

// tsEnumName returns the TS name for a Go enum type.
func tsEnumName(goName string) string {
	if override, ok := enumTSName[goName]; ok {
		return override
	}
	return goName
}

// decoderName returns the decoder function name for a type.
func decoderName(typeName string) string {
	return "decode" + tsName(typeName)
}

// pathName returns the $.path prefix for a type (snake_case of the type name).
// Handles acronyms: MCPConnectedPayload → mcp_connected_payload.
func pathName(typeName string) string {
	if override, ok := pathNameOverride[typeName]; ok {
		return override
	}
	var b strings.Builder
	runes := []rune(typeName)
	for i, r := range runes {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				prev := runes[i-1]
				if prev >= 'a' && prev <= 'z' {
					b.WriteByte('_')
				} else if prev >= 'A' && prev <= 'Z' && i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z' {
					b.WriteByte('_')
				}
			}
			b.WriteRune(r + 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// pathNameOverride handles types where automatic conversion doesn't match
// the existing hand-written decoder path conventions.
var pathNameOverride = map[string]string{
	"MCPOAuthPayload": "mcp_oauth_payload",
	"RepoEntry":       "repo_entry",
}

// enumConstName returns the SCREAMING_SNAKE const name for an enum.
func enumConstName(goTypeName string) string {
	name := tsEnumName(goTypeName)
	var b strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				prev := runes[i-1]
				if prev >= 'a' && prev <= 'z' {
					b.WriteByte('_')
				} else if prev >= 'A' && prev <= 'Z' && i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z' {
					b.WriteByte('_')
				}
			}
			b.WriteRune(r)
		} else {
			b.WriteRune(r - 32)
		}
	}
	b.WriteString("S")
	return b.String()
}

// isPrimitive returns true if the type maps to a TS primitive (string/number/boolean).
func isPrimitive(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		return isPrimitive(t.Elem())
	}
	if t == reflect.TypeFor[time.Time]() {
		return true
	}
	switch t.Kind() {
	case reflect.String:
		return true
	case reflect.Bool:
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// isEnum returns true if the type is a named string type in the Enums map.
func isEnum(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		return isEnum(t.Elem())
	}
	_, ok := Enums[t.Name()]
	return ok && t.Kind() == reflect.String
}

// isStruct returns true if the type (after unwrapping ptr) is a registered struct.
func isStruct(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		return isStruct(t.Elem())
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	_, ok := typeByName[t.Name()]
	return ok
}

// isRawMessage returns true for json.RawMessage.
func isRawMessage(t reflect.Type) bool {
	return t == reflect.TypeFor[json.RawMessage]()
}

// isInterface returns true for interface{}/any.
func isInterface(t reflect.Type) bool {
	return t.Kind() == reflect.Interface
}

// primHelper returns the reqXxx/optXxx helper name for a primitive type.
func primHelper(t reflect.Type, optional bool) string {
	if t.Kind() == reflect.Pointer {
		return primHelper(t.Elem(), optional)
	}
	if t == reflect.TypeFor[time.Time]() {
		if optional {
			return "optStr"
		}
		return "reqStr"
	}
	prefix := "req"
	if optional {
		prefix = "opt"
	}
	switch t.Kind() {
	case reflect.String:
		return prefix + "Str"
	case reflect.Bool:
		return prefix + "Bool"
	default:
		return prefix + "Num"
	}
}

// elemDecoderExpr returns the decoder expression for an element type (used in decodeArray/decodeRecord).
func elemDecoderExpr(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if isStruct(t) {
		return decoderName(t.Name())
	}
	if t.Kind() == reflect.String {
		return "(v) => { if (typeof v !== \"string\") throw new TypeError(\"expected string\"); return v as string; }"
	}
	if t.Kind() == reflect.Interface {
		return tsIdentityCast

	}
	if isRawMessage(t) {
		return tsIdentityCast

	}
	if t.Kind() == reflect.Map {
		return "(v) => asObject(v)"
	}
	if t.Name() != "" {
		return decoderName(t.Name())
	}
	return tsIdentityCast

}

// generateTypes writes types.gen.ts.
func generateTypes(w *strings.Builder) {
	w.WriteString("// CODE-GENERATED by cmd/wire-codegen, DO NOT EDIT.\n\n")

	// Emit enum types (sorted by TS name, deduplicated).
	enumNames := make([]string, 0, len(Enums))
	seenEnumTS := map[string]bool{}
	for name := range Enums {
		tn := tsEnumName(name)
		if seenEnumTS[tn] {
			continue
		}
		seenEnumTS[tn] = true
		enumNames = append(enumNames, name)
	}
	sort.Slice(enumNames, func(i, j int) bool { return tsEnumName(enumNames[i]) < tsEnumName(enumNames[j]) })
	for _, name := range enumNames {
		def := Enums[name]
		w.WriteString("export type " + tsEnumName(name) + " = ")
		for i, v := range def.Values {
			if i > 0 {
				w.WriteString(" | ")
			}
			w.WriteString("\"" + v + "\"")
		}
		w.WriteString(";\n\n")
	}

	// Emit struct interfaces (sorted alphabetically by TS name).
	names := make([]string, 0, len(WireTypes))
	for _, t := range WireTypes {
		names = append(names, t.Name())
	}
	// Sort by TS name.
	sort.Slice(names, func(i, j int) bool { return tsName(names[i]) < tsName(names[j]) })
	for _, name := range names {
		t := typeByName[name]
		fields := parseFields(t)
		w.WriteString("export interface " + tsName(name) + " {\n")
		for _, f := range fields {
			ts := tsType(f.goType)
			if f.optional {
				w.WriteString("  " + f.wireName + "?: " + ts + ";\n")
			} else {
				w.WriteString("  " + f.wireName + ": " + ts + ";\n")
			}
		}
		w.WriteString("}\n\n")
	}
}

// generateDecoders writes decoders.gen.ts.
//
//nolint:gocyclo // type-switch over reflect kinds is inherently branchy
func generateDecoders(w *strings.Builder) {
	// Generate decoder bodies first, to a separate buffer, so we can
	// scan them and emit only the imports/consts that are actually
	// referenced. This keeps the generated file free of "declared but
	// never used" lints under noUnusedLocals.
	var bodies strings.Builder
	goNames := make([]string, 0, len(WireTypes))
	for _, t := range WireTypes {
		goNames = append(goNames, t.Name())
	}
	sort.Slice(goNames, func(i, j int) bool { return tsName(goNames[i]) < tsName(goNames[j]) })
	for _, name := range goNames {
		t := typeByName[name]
		emitDecoder(&bodies, name, t)
	}
	body := bodies.String()

	// Header.
	w.WriteString("// CODE-GENERATED by cmd/wire-codegen, DO NOT EDIT.\n\n")
	allHelpers := []string{
		"asObject", "asArray", "reqStr", "reqNum", "reqBool",
		"optStr", "optNum", "optBool", "reqOneOf",
		"decodeArray", "decodeRecord",
	}
	usedHelpers := []string{}
	for _, h := range allHelpers {
		if isIdentReferenced(body, h) {
			usedHelpers = append(usedHelpers, h)
		}
	}
	w.WriteString("import { ")
	if len(usedHelpers) > 0 {
		w.WriteString(strings.Join(usedHelpers, ", "))
		w.WriteString(", ")
	}
	w.WriteString("type Decoder } from \"../validators.js\";\n")

	// Collect all type names that need to be imported, then keep only
	// those whose identifier actually appears in the generated bodies.
	candidateNames := make([]string, 0, len(WireTypes))
	for _, t := range WireTypes {
		candidateNames = append(candidateNames, tsName(t.Name()))
	}
	enumSeen := map[string]bool{}
	for name := range Enums {
		tn := tsEnumName(name)
		if !enumSeen[tn] {
			enumSeen[tn] = true
			candidateNames = append(candidateNames, tn)
		}
	}
	usedSet := map[string]bool{}
	for _, n := range candidateNames {
		if isIdentReferenced(body, n) {
			usedSet[n] = true
		}
	}
	used := make([]string, 0, len(usedSet))
	for n := range usedSet {
		used = append(used, n)
	}
	sort.Strings(used)
	if len(used) > 0 {
		w.WriteString("import type { ")
		w.WriteString(strings.Join(used, ", "))
		w.WriteString(" } from \"./types.gen.js\";\n")
	}
	w.WriteString("\n")

	// Emit enum const arrays — only the ones referenced in bodies.
	emitted := map[string]bool{}
	for _, name := range enumNamesSlice(Enums) {
		constN := enumConstName(name)
		if emitted[constN] {
			continue
		}
		if !isIdentReferenced(body, constN) {
			continue
		}
		emitted[constN] = true
		def := Enums[name]
		w.WriteString("const " + constN + " = [")
		for i, v := range def.Values {
			if i > 0 {
				w.WriteString(", ")
			}
			w.WriteString("\"" + v + "\"")
		}
		w.WriteString("] as const;\n")
	}
	if len(emitted) > 0 {
		w.WriteString("\n")
	}

	w.WriteString(body)
}

// isIdentReferenced reports whether `ident` appears as a whole-word
// identifier in body. Used to filter out unused imports/consts in the
// generated file.
func isIdentReferenced(body, ident string) bool {
	for i := 0; i < len(body); {
		j := strings.Index(body[i:], ident)
		if j < 0 {
			return false
		}
		j += i
		// Check the character before is not an identifier char.
		if j > 0 {
			c := body[j-1]
			if isIdentChar(c) {
				i = j + len(ident)
				continue
			}
		}
		// Check the character after is not an identifier char.
		end := j + len(ident)
		if end < len(body) {
			c := body[end]
			if isIdentChar(c) {
				i = end
				continue
			}
		}
		return true
	}
	return false
}

func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$'
}

func enumNamesSlice(m map[string]EnumDef) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func emitDecoder(w *strings.Builder, name string, t reflect.Type) {
	fields := parseFields(t)
	tn := tsName(name)
	path := "$." + pathName(tn)
	w.WriteString("export const " + decoderName(name) + ": Decoder<" + tn + "> = (v) => {\n")
	w.WriteString("  const o = asObject(v, \"" + path + "\");\n")

	// Separate required and optional fields.
	var reqFields, optFields []fieldInfo
	for _, f := range fields {
		if f.optional {
			optFields = append(optFields, f)
		} else {
			reqFields = append(reqFields, f)
		}
	}

	// Build the initial object literal with required fields.
	if len(reqFields) > 0 || len(optFields) > 0 {
		w.WriteString("  const out: " + tn + " = {\n")
		for _, f := range reqFields {
			w.WriteString("    " + f.wireName + ": " + reqExpr(f, path) + ",\n")
		}
		w.WriteString("  };\n")
	} else {
		w.WriteString("  const out: " + tn + " = {};\n")
	}

	// Handle optional fields.
	for _, f := range optFields {
		emitOptionalField(w, f, path)
	}

	w.WriteString("  return out;\n")
	w.WriteString("};\n\n")
}

// reqExpr returns the expression for a required field in the initial literal.
func reqExpr(f fieldInfo, path string) string {
	t := f.goType
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// json.RawMessage → passthrough
	if isRawMessage(t) {
		return "o[\"" + f.wireName + "\"] as unknown"
	}
	// interface{} → passthrough
	if isInterface(t) {
		return "o[\"" + f.wireName + "\"] as unknown"
	}
	// Enum
	if isEnum(t) {
		return "reqOneOf(o, \"" + f.wireName + "\", " + enumConstName(t.Name()) + ", \"" + path + "\")"
	}
	// Primitive
	if isPrimitive(t) {
		return primHelper(t, false) + "(o, \"" + f.wireName + "\", \"" + path + "\")"
	}
	// Struct
	if isStruct(t) {
		return decoderName(t.Name()) + "(o[\"" + f.wireName + "\"])"
	}
	// Slice
	if t.Kind() == reflect.Slice {
		elem := t.Elem()
		if elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		return "decodeArray(o[\"" + f.wireName + "\"], " + elemDecoderExpr(elem) + ", \"" + path + "." + f.wireName + "\")"
	}
	// Map
	if t.Kind() == reflect.Map {
		valType := t.Elem()
		return "decodeRecord(o[\"" + f.wireName + "\"], " + elemDecoderExpr(valType) + ", \"" + path + "." + f.wireName + "\")"
	}
	return "o[\"" + f.wireName + "\"] as unknown"
}

// emitOptionalField emits the if-check for an optional field.
func emitOptionalField(w *strings.Builder, f fieldInfo, path string) {
	t := f.goType
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// json.RawMessage → passthrough
	if isRawMessage(t) {
		w.WriteString("  if (o[\"" + f.wireName + "\"] !== undefined) out." + f.wireName + " = o[\"" + f.wireName + "\"] as unknown;\n")
		return
	}
	// interface{} → passthrough
	if isInterface(t) {
		w.WriteString("  if (o[\"" + f.wireName + "\"] !== undefined) out." + f.wireName + " = o[\"" + f.wireName + "\"] as unknown;\n")
		return
	}
	// Enum (optional)
	if isEnum(t) {
		w.WriteString("  if (o[\"" + f.wireName + "\"] !== undefined) out." + f.wireName + " = reqOneOf(o, \"" + f.wireName + "\", " + enumConstName(t.Name()) + ", \"" + path + "\");\n")
		return
	}
	// Primitive (optional)
	if isPrimitive(t) {
		helper := primHelper(t, true)
		varName := sanitizeVarName(f.wireName)
		w.WriteString("  const " + varName + " = " + helper + "(o, \"" + f.wireName + "\", \"" + path + "\");\n")
		w.WriteString("  if (" + varName + " !== undefined) out." + f.wireName + " = " + varName + ";\n")
		return
	}
	// Struct (optional)
	if isStruct(t) {
		w.WriteString("  if (o[\"" + f.wireName + "\"] !== undefined) out." + f.wireName + " = " + decoderName(t.Name()) + "(o[\"" + f.wireName + "\"]);\n")
		return
	}
	// Slice (optional)
	if t.Kind() == reflect.Slice {
		elem := t.Elem()
		if elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		w.WriteString("  if (o[\"" + f.wireName + "\"] !== undefined) out." + f.wireName + " = decodeArray(o[\"" + f.wireName + "\"], " + elemDecoderExpr(elem) + ", \"" + path + "." + f.wireName + "\");\n")
		return
	}
	// Map (optional)
	if t.Kind() == reflect.Map {
		valType := t.Elem()
		w.WriteString("  if (o[\"" + f.wireName + "\"] !== undefined) out." + f.wireName + " = decodeRecord(o[\"" + f.wireName + "\"], " + elemDecoderExpr(valType) + ", \"" + path + "." + f.wireName + "\");\n")
		return
	}
	w.WriteString("  if (o[\"" + f.wireName + "\"] !== undefined) out." + f.wireName + " = o[\"" + f.wireName + "\"] as unknown;\n")
}

// sanitizeVarName converts a wire name to a valid JS variable name (camelCase).
func sanitizeVarName(wireName string) string {
	parts := strings.Split(wireName, "_")
	var b strings.Builder
	for i, p := range parts {
		if i == 0 {
			b.WriteString(p)
		} else if p != "" {
			b.WriteString(strings.ToUpper(p[:1]) + p[1:])
		}
	}
	s := b.String()
	// Avoid collisions with "o", "out", "v" and JS reserved words.
	switch s {
	case "o", "out", "v", "private", "public", "protected", "class",
		"return", enumDelete, "default", "export", "import", "new", "this":
		return s + "Val"
	}
	return s
}

// generateRegistry writes registry.gen.ts.
func generateRegistry(w *strings.Builder) {
	w.WriteString("// CODE-GENERATED by cmd/wire-codegen, DO NOT EDIT.\n\n")
	w.WriteString("import { registerSSEDecoder } from \"../bus.js\";\n")

	// Collect unique decoder names needed.
	decoderImports := make([]string, 0)
	seen := map[string]bool{}
	for _, e := range SSEEvents {
		dn := decoderName(e.TypeName)
		if !seen[dn] {
			seen[dn] = true
			decoderImports = append(decoderImports, dn)
		}
	}
	sort.Strings(decoderImports)
	w.WriteString("import { " + strings.Join(decoderImports, ", ") + " } from \"./decoders.gen.js\";\n\n")

	w.WriteString("export function registerAllSSEDecoders(): void {\n")
	// SSEEvents is already sorted by EventType.
	for _, e := range SSEEvents {
		w.WriteString("  registerSSEDecoder(\"" + e.EventType + "\", " + decoderName(e.TypeName) + ");\n")
	}
	w.WriteString("}\n")
}

func main() {
	outDir := filepath.Join("static-src", "wire")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", outDir, err)
		os.Exit(1)
	}

	// Generate types.
	var typesBuf strings.Builder
	generateTypes(&typesBuf)
	if err := os.WriteFile(filepath.Join(outDir, "types.gen.ts"), []byte(typesBuf.String()), 0o644); err != nil { //nolint:gosec // G306: generated source file
		fmt.Fprintf(os.Stderr, "write types.gen.ts: %v\n", err)
		os.Exit(1)
	}

	// Generate decoders.
	var decodersBuf strings.Builder
	generateDecoders(&decodersBuf)
	if err := os.WriteFile(filepath.Join(outDir, "decoders.gen.ts"), []byte(decodersBuf.String()), 0o644); err != nil { //nolint:gosec // G306: generated source file
		fmt.Fprintf(os.Stderr, "write decoders.gen.ts: %v\n", err)
		os.Exit(1)
	}

	// Generate registry.
	var registryBuf strings.Builder
	generateRegistry(&registryBuf)
	if err := os.WriteFile(filepath.Join(outDir, "registry.gen.ts"), []byte(registryBuf.String()), 0o644); err != nil { //nolint:gosec // G306: generated source file
		fmt.Fprintf(os.Stderr, "write registry.gen.ts: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("wire-codegen: generated static-src/wire/{types,decoders,registry}.gen.ts")
}
