package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Format types.
const (
	FormatJSON  = "json"
	FormatText  = "text"
	FormatTable = "table"
	FormatTSV   = "tsv"
)

// TableData represents data that can be rendered as a table.
type TableData struct {
	Headers []string
	Rows    [][]string
}

// Print outputs data in the specified format to w.
// data can be any type. For table/tsv, data should be a TableData.
func Print(w io.Writer, format string, data interface{}) error {
	switch format {
	case FormatJSON:
		return printJSON(w, data)
	case FormatText:
		return printText(w, data)
	case FormatTable:
		return printTable(w, data)
	case FormatTSV:
		return printTSV(w, data)
	default:
		return fmt.Errorf("unknown format: %s", format)
	}
}

// Text outputs a simple string message.
func Text(w io.Writer, msg string) {
	fmt.Fprintln(w, msg)
}

func printJSON(w io.Writer, data interface{}) error {
	// For TableData, convert to []map[string]string using headers as keys.
	if td, ok := data.(TableData); ok {
		rows := make([]map[string]string, 0, len(td.Rows))
		for _, row := range td.Rows {
			m := make(map[string]string, len(td.Headers))
			for j, header := range td.Headers {
				if j < len(row) {
					m[strings.ToLower(strings.ReplaceAll(header, " ", "_"))] = row[j]
				}
			}
			rows = append(rows, m)
		}
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling JSON: %w", err)
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func printText(w io.Writer, data interface{}) error {
	switch v := data.(type) {
	case TableData:
		return printTextTable(w, v)
	case string:
		_, err := fmt.Fprintln(w, v)
		return err
	default:
		return printJSON(w, data)
	}
}

func printTextTable(w io.Writer, td TableData) error {
	for i, row := range td.Rows {
		if i > 0 {
			fmt.Fprintln(w)
		}
		for j, cell := range row {
			header := ""
			if j < len(td.Headers) {
				header = td.Headers[j]
			}
			fmt.Fprintf(w, "%s: %s\n", header, cell)
		}
	}
	return nil
}

func printTable(w io.Writer, data interface{}) error {
	td, ok := data.(TableData)
	if !ok {
		return printJSON(w, data)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(td.Headers, "\t"))
	for _, row := range td.Rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}

func printTSV(w io.Writer, data interface{}) error {
	td, ok := data.(TableData)
	if !ok {
		return printJSON(w, data)
	}
	fmt.Fprintln(w, strings.Join(td.Headers, "\t"))
	for _, row := range td.Rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	return nil
}
