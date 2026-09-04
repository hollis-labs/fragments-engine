# Reader resource serving

Reader projections keep full immutable content and large media as separately
fetched resources. The frozen v1 fixtures established the resource URL shapes,
although the frozen OpenAPI document does not yet enumerate these paths.
Fragments Engine implements the fixture-compatible paths without changing the
frozen contract artifacts:

- `GET|HEAD /v1/reader/items/{fragmentId}/content?revision_id=...` serves
  allowlist-sanitized HTML by default. `format=markdown` or
  `Accept: text/markdown` serves the exact normalized source Markdown.
- `GET|HEAD /v1/media/variants/{variantId}/content?fragment_id=...&revision_id=...`
  serves an FE-custodied representation only when the logical asset is attached
  to that exact immutable revision.

The media query context is intentionally stronger than the early fixture
example, which contains only a variant ID. Generated `content_href` values add
the canonical fragment and immutable revision IDs. A fragment alias resolves to
its canonical fragment, but never widens revision or variant ownership.

Both resources are currently loopback-only because the API has no general token
authentication layer. This is the local personal deployment authorization
profile; a future authenticated profile can replace the transport gate without
weakening revision and variant ownership checks.

## Article trust boundary

Normalized Markdown remains the canonical searchable content. HTML is generated
server-side and passed through a closed element, attribute, and URL-scheme
allowlist. Raw HTML, scripts, styles, event handlers, images, embeds, iframes,
and provider/oEmbed markup cannot survive into rendered output. Markdown is
served with `nosniff`, an inline-safe disposition, and a restrictive CSP so its
source text is not interpreted as a browser document.

Revision content is checked against its stored SHA-256 digest. Both HTML and
Markdown responses expose a digest and strong ETag for the exact response bytes.
Because a revision is immutable, these responses are private and immutable.

## Media trust boundary

Content-addressed blobs are opened only through their opaque blob-store handle.
The complete file is checked against its recorded size and SHA-256 identity
before any bytes are served. At most one bounded byte range is accepted; invalid
or multipart ranges are rejected.

Compatibility media without a content-addressed blob is readable only from its
persisted `legacy_storage_path`, only for adopted/available variants, and only
when the real regular file remains beneath `reviewer.download_root` without a
symlink. On Darwin and Linux, FE anchors traversal at an open root directory and
opens every descendant component with `openat` and `O_NOFOLLOW`; legacy serving
is disabled on platforms where this stable no-follow open is not implemented.
Request values never become filesystem paths. `source_path` and `source_url` are
provenance and are never opened, proxied, or redirected.
Compatibility paths are mutable and therefore use `Cache-Control: no-store`.

MIME selection uses received bytes plus a closed family appropriate to the
asset and variant. JPEG, PNG, GIF, WebP, MP4, WebM, MP3, WAV, PDF, plain
Markdown/text, VTT, and SRT are supported. Ogg and AVIF are not claimed because
Go's byte sniffer does not distinguish them into the required safe media family.
SVG, HTML, XML, JavaScript, an unrecognized byte stream, or a declaration/byte
mismatch is never served.
Pending, reference-only, and failed variants return an explicit
`capability_unavailable` problem; a missing or non-owned resource returns the
same 404 response.

## Playback

Reader playback uses one strict constructor for the closed `PlaybackSpec`. It
accepts only a stored YouTube provider identity and an eleven-character
allowlisted provider item ID, validated by the provider contract. Source URLs,
external streams, and oEmbed HTML never become playback instructions. The
Reader UI owns construction of the restrictive YouTube iframe, CSP/permissions,
and fullscreen behavior.
