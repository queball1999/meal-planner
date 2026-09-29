package settings

// Embeds the IANA zone database so every entry below resolves even where the
// host has no zoneinfo (Windows dev boxes, minimal containers).
import _ "time/tzdata"

// Timezones is the APP_TIMEZONE option list: UTC, then common IANA zones
// grouped by region. The Settings page renders it as a searchable Choices.js
// dropdown. TestTimezonesResolve checks every entry loads.
var Timezones = []string{
	"UTC",

	// North America
	"America/New_York", "America/Detroit", "America/Indiana/Indianapolis",
	"America/Kentucky/Louisville", "America/Chicago", "America/Indiana/Knox",
	"America/Menominee", "America/North_Dakota/Center", "America/Denver",
	"America/Boise", "America/Phoenix", "America/Los_Angeles", "America/Anchorage",
	"America/Juneau", "America/Adak", "Pacific/Honolulu", "America/Puerto_Rico",
	"America/Toronto", "America/Winnipeg", "America/Edmonton", "America/Vancouver",
	"America/Halifax", "America/St_Johns", "America/Regina",
	"America/Mexico_City", "America/Tijuana", "America/Cancun", "America/Monterrey",

	// Central / South America & Caribbean
	"America/Guatemala", "America/Costa_Rica", "America/Panama", "America/Havana",
	"America/Jamaica", "America/Santo_Domingo", "America/Bogota", "America/Lima",
	"America/Caracas", "America/Santiago", "America/La_Paz",
	"America/Argentina/Buenos_Aires", "America/Montevideo", "America/Asuncion",
	"America/Sao_Paulo", "America/Manaus",

	// Europe
	"Europe/London", "Europe/Dublin", "Europe/Lisbon", "Europe/Madrid",
	"Europe/Paris", "Europe/Brussels", "Europe/Amsterdam", "Europe/Berlin",
	"Europe/Zurich", "Europe/Rome", "Europe/Vienna", "Europe/Prague",
	"Europe/Warsaw", "Europe/Budapest", "Europe/Stockholm", "Europe/Oslo",
	"Europe/Copenhagen", "Europe/Helsinki", "Europe/Athens", "Europe/Bucharest",
	"Europe/Sofia", "Europe/Kyiv", "Europe/Istanbul", "Europe/Moscow",
	"Atlantic/Reykjavik", "Atlantic/Azores", "Atlantic/Canary",

	// Africa & Middle East
	"Africa/Casablanca", "Africa/Lagos", "Africa/Accra", "Africa/Cairo",
	"Africa/Johannesburg", "Africa/Nairobi", "Africa/Addis_Ababa",
	"Asia/Jerusalem", "Asia/Beirut", "Asia/Amman", "Asia/Baghdad",
	"Asia/Riyadh", "Asia/Qatar", "Asia/Dubai", "Asia/Tehran",

	// Asia
	"Asia/Karachi", "Asia/Tashkent", "Asia/Kolkata", "Asia/Kathmandu",
	"Asia/Dhaka", "Asia/Yangon", "Asia/Bangkok", "Asia/Ho_Chi_Minh",
	"Asia/Jakarta", "Asia/Kuala_Lumpur", "Asia/Singapore", "Asia/Manila",
	"Asia/Hong_Kong", "Asia/Shanghai", "Asia/Taipei", "Asia/Seoul",
	"Asia/Tokyo", "Asia/Vladivostok",

	// Oceania
	"Australia/Perth", "Australia/Darwin", "Australia/Adelaide",
	"Australia/Brisbane", "Australia/Sydney", "Australia/Melbourne",
	"Australia/Hobart", "Pacific/Guam", "Pacific/Noumea", "Pacific/Auckland",
	"Pacific/Fiji", "Pacific/Tongatapu", "Pacific/Pago_Pago",
}
