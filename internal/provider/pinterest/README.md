# Pinterest provider adapter

`pinterest.Adapter` is a stateless W2.2 identity, metadata, and media adapter
over an injected typed client. It emits only the shared fragment capabilities
and generic media types.

Direct pin URLs are canonicalized to
`https://www.pinterest.com/pin/{numeric-id}/`. A `pin.it` short code is only a
lookup reference: it never becomes identity, and the client must return an
allowlisted final Pinterest pin URL whose numeric ID agrees with the response.

The entities observation encodes the stable pin as
`{"kind":"pinterest_pin","value":"{numeric-id}"}`, with optional
`creator` and `pinterest_board` entries. Title, description, and tags use the
shared typed observation shapes.

The client explicitly labels original and thumbnail candidates. The adapter
passes those candidates through without resizing or promoting one into the
other. Candidate bytes remain pending/reference-only/failed in generic
`AssetVariant` state; a logical candidate does not imply successful custody.
Representation URLs must use HTTPS without userinfo or custom ports and an
allowlisted Pinterest or `pinimg.com` host.
Each call emits only its claimed capability. Failed variants remain in the
generic manifest but are excluded from representation coverage; a failure-only
result is classified retryable when any matching provider failure is retryable.
Signed representation expiry is copied to the capability observation so the
shared planner can refresh it without replaying capture.

Raw oEmbed JSON is an input-only client field. It is narrowed through the
shared safe oEmbed parser, and its `html` or unknown properties never appear in
adapter results. Client causes are not copied into durable error messages.
