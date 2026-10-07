package release

// builtinKeys are the project's release signing keys ("id:base64"). The
// private halves are kept as the TAPER_SIGNING_KEY GitHub Actions secret,
// and offline by the project owner.
var builtinKeys = []string{
	"taper-2026:ZZwAcY1xva7o2ZJymMpAmOWPUlT021raQHrTsZKZVUE=",
}
