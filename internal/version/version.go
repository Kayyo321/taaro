package version

// Version is the semantic version of Taaro, overrideable at build time via -ldflags.
var Version = "1.0.0"

// ProtocolVersion identifies the Taaro Adapter Protocol wire version.
const ProtocolVersion = "1"
