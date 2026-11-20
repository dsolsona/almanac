# SLI Measurements range screenshots

These screenshots were captured in Chromium at 1440 × 1000 on the deployed
Almanac collection test stack. A real agent established the SLI and its first
observations. The isolated PostgreSQL fixture supplied 32 days of synthetic
15-second event counts with varying ratios and gaps. All six views used the
normal authenticated UI and API.

| Range | Screenshot |
| --- | --- |
| 1 hour | [1h.png](1h.png) |
| 3 hours | [3h.png](3h.png) |
| 6 hours | [6h.png](6h.png) |
| 24 hours | [24h.png](24h.png) |
| 7 days | [7d.png](7d.png) |
| 30 days | [30d.png](30d.png) |

[Implementation PR #159](https://github.com/dsolsona/almanac/pull/159)
