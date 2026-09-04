# Instagram provider adapter

`instagram.Adapter` is a stateless W2.2 identity, metadata, and media adapter
over an injected typed client. Public and authenticated access profiles declare
their network class and credential reference names; credential bytes stay in
the injected client/resolver boundary.

Post and reel URL variants are canonicalized to
`https://www.instagram.com/p/{id}/` or
`https://www.instagram.com/reel/{id}/`. The entities observation encodes the
stable item as `instagram_post` or `instagram_reel`, with an optional
`instagram_author` entity. Caption is a description observation and tags use
the shared typed set value.

Carousel order exists only in the returned `AttachmentRef.Position` values and
the ordered gallery observation. Every media asset uses a stable provider item
ID, so reordering never changes media or variant identity. Images may expose
original/thumbnail candidates. Videos remain reference custody and may expose
an original stream reference plus a separately mirrorable poster.

Representation URLs must use HTTPS without userinfo or custom ports and an
allowlisted Instagram, `cdninstagram.com`, or `fbcdn.net` host.

Typed unavailable variants become generic failed `AssetVariant` values with a
safe code, fixed message, and retryability. Usable siblings remain present.
Unsupported arbitrary content, missing authorization, rate limiting, and
transient access are distinct classified adapter errors.
Each call emits only its claimed capability. Failed variants are excluded from
original/poster coverage while the logical carousel and successful siblings
remain available. A failure-only representation result retains the manifest
and reports retryability honestly. Signed representation expiry is copied to
the observation for shared stale refresh planning.

Raw oEmbed JSON is input-only and narrowed through the shared safe parser. Its
`html` and unknown fields never appear in results, media, or observations.
