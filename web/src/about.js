// Where the project lives. A fixed fact about the software, not something an operator tunes, so it is a constant, not a setting.
export const REPO = 'https://github.com/railadeividas/iptv-proxy'

// The release page of a version such as "v1.2.0"; builds that are not a release (dev-…, edge-…) go to the project page.
export const versionUrl = (v) => (/^v\d/.test(v) ? `${REPO}/releases/tag/${v}` : REPO)
