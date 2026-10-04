---
kind: changed
---

**PR status in the session tree reads GitHub directly.** It uses `HIVE_GITHUB_TOKEN`, then any GitHub account connected in Hive Desktop, then your `gh` login, so `gh` is no longer required. A failed lookup no longer hides a session's PR badge until the cache expires.
