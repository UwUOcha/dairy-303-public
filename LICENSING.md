# Licensing

Copyright © 2026 Mikhail (UwUOcha).

The platform is licensed under **GNU AGPL version 3 only** (`AGPL-3.0-only`),
as provided in [LICENSE](LICENSE). The choice does not grant an option to use
later versions of that license.

The adapter contract library in [`pkg/provider`](pkg/provider/) is separately
licensed under [MIT](pkg/provider/LICENSE). This includes its Go types, validation,
HTTP client/server. Independent adapters may reuse that
library under MIT without adopting the platform's AGPL license.

Third-party dependencies retain their own licenses; see
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

When providing a modified version of the platform over a network, prominently
offer its users the corresponding source of that version as required by AGPL
section 13. The built-in `/source` page exposes the configured source URL and
license. Set `SOURCE_CODE_URL` at build time or `WEB_SOURCE_CODE_URL` at runtime
to the source of the actual version you operate. A link to an unrelated or older
upstream version is not a substitute for the source of your modifications.

User records, API credentials and other runtime data are not part of the source
release. This document does not grant rights to third-party university branding.


