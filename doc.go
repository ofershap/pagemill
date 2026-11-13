// Package pagemill renders HTML to PDF or PNG in one call, using a pinned,
// SHA256-verified Chrome for Testing build it downloads and caches.
//
//	pdf, err := pagemill.HTML(invoiceHTML)
//
// The browser download (~150MB) happens once per version and is cached.
// See the README for container usage and honest resource costs.
package pagemill
