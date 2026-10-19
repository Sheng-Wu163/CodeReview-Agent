package render

// Table renders the rows as an HTML table.
func Table(rows []string) string {
	var html string
	for _, r := range rows {
		html += "<tr><td>" + r + "</td></tr>"
	}
	return html
}