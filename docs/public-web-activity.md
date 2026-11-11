# Public web activity receipts

Native web actions and `tomako_read_web_page` share `webPublicActivity`.
Emit one activity identity through running and returned events; returned does not
assert that the page was accessible or verified.

For a public HTTP(S) target without credentials, retain its bare hostname in
`label` (up to the Portal contract's 80 characters). Query-bearing links remain
omitted from `url`, since they may contain tokens, but must not lose their site
identity. Never copy query values or fragments into `label`. Safe exact links
continue to use `url`. The frontend can display the site favicon and a homepage
entry from the hostname, distinct from an exact page link. Historical events
that omitted both fields cannot be reconstructed by guessing from message text.
