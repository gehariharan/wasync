package cli

import (
	"strconv"
	"strings"

	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/output"
)

var fieldsFlag = flagSpec{name: "--fields", value: "LIST", description: "comma-separated text columns; see the defaults and available fields below; --json returns the complete v1 data instead"}

func listFieldsFlag(available, defaults []string) flagSpec {
	flag := fieldsFlag
	flag.description = "text columns: " + fieldsDescription(available, defaults) + "; cannot combine with --json"
	return flag
}

type columns struct {
	names   []string
	indexes []int
}

func selectColumns(inv invocation, available, defaults []string) (columns, error) {
	selected := defaults
	if value, ok := inv.flag("--fields"); ok {
		if inv.json {
			return columns{}, usageErr("use --fields for text output, or --json for complete v1 data", "--fields cannot be combined with --json")
		}
		selected = strings.Split(value, ",")
	}
	result := columns{}
	seen := map[string]bool{}
	for _, name := range selected {
		name = strings.TrimSpace(name)
		index := -1
		for i, candidate := range available {
			if name == candidate {
				index = i
				break
			}
		}
		if index < 0 || seen[name] {
			return columns{}, usageErr("available fields: "+strings.Join(available, ","), "invalid or repeated field %q", name)
		}
		seen[name] = true
		result.names = append(result.names, name)
		result.indexes = append(result.indexes, index)
	}
	return result, nil
}

func (c columns) has(name string) bool {
	for _, n := range c.names {
		if n == name {
			return true
		}
	}
	return false
}

func (c columns) table(o *output.Writer, indent int, name string, rows [][]string) {
	projected := make([][]string, 0, len(rows))
	for _, row := range rows {
		item := make([]string, len(c.indexes))
		for i, index := range c.indexes {
			item[i] = row[index]
		}
		projected = append(projected, item)
	}
	o.TableCells(indent, name, c.names, projected)
}

// writeResultCount keeps a page count distinct from the full matching total.
// Older owners do not supply a total: unknown is truthful, including on empty
// pages produced by authorization filtering.
func writeResultCount(o *output.Writer, shown int, total *int64, page *contract.Page) {
	o.Value(0, "shown", shown)
	if total != nil {
		o.Value(0, "total", *total)
	} else {
		o.Field(0, "total", "unknown")
	}
	if page != nil {
		o.Value(0, "has_more", page.HasMore)
	}
}

func chatAggregates(o *output.Writer, indent int, chats []contract.Chat) {
	groups, enabled := 0, 0
	var messages int64
	for _, chat := range chats {
		if chat.Kind == "group" {
			groups++
		}
		if chat.SyncEnabled {
			enabled++
		}
		messages += chat.MessageCount
	}
	o.Value(indent, "total", len(chats))
	o.Value(indent, "groups", groups)
	o.Value(indent, "direct", len(chats)-groups)
	o.Value(indent, "capture_enabled", enabled)
	o.Value(indent, "archived_messages", messages)
}

func fieldsDescription(available, defaults []string) string {
	return "default " + strings.Join(defaults, ",") + "; available " + strings.Join(available, ",")
}

func allChatsHint(total int) string {
	return "Run `wasync-axi chats --limit 0` for all " + strconv.Itoa(total) + " archived chats"
}
