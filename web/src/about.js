// Where the project lives. A fixed fact about the software, not something an operator tunes, so it is a constant, not a setting.
export const REPO = 'https://github.com/railadeividas/iptv-proxy'

// Where to read about a version: an image build is a commit hash ("ef711d2") and opens that commit; older release tags
// ("v1.2.0") open their release page; anything else (dev-…) goes to the project page.
export const versionUrl = (v) => (/^[0-9a-f]{7,40}$/.test(v) ? `${REPO}/commit/${v}` : /^v\d/.test(v) ? `${REPO}/releases/tag/${v}` : REPO)
