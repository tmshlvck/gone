package site

// CommonZones is a curated list of IANA timezone names covering the major
// regions, for TimezonePicker's full-menu (pass it as Zones). It is not
// exhaustive — apps with niche needs supply their own slice. Ordered roughly
// west-to-east so the dropdown reads geographically.
var CommonZones = []string{
	"Pacific/Honolulu",
	"America/Anchorage",
	"America/Los_Angeles",
	"America/Denver",
	"America/Chicago",
	"America/New_York",
	"America/Sao_Paulo",
	"Atlantic/Reykjavik",
	"Europe/London",
	"Europe/Paris",
	"Europe/Zurich",
	"Europe/Berlin",
	"Europe/Athens",
	"Europe/Moscow",
	"Africa/Cairo",
	"Africa/Johannesburg",
	"Asia/Dubai",
	"Asia/Kolkata",
	"Asia/Bangkok",
	"Asia/Shanghai",
	"Asia/Singapore",
	"Asia/Tokyo",
	"Australia/Sydney",
	"Pacific/Auckland",
}
