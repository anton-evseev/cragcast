# Weather scoring

Cragcast fetches seven calendar days of hourly weather history and seven days
of forecast from Open-Meteo. The history is model weather, not a rain gauge
reading at the rock. Each crag receives its own copy of the weather
series. Two moisture estimates start dry at the beginning of the history and
advance chronologically through every hour, including nights. These defaults
are **uncalibrated product estimates**, not observations of rock conditions.

## Rain and drying history

Surface wetness is bounded from zero to two and seepage potential from zero to
one. For each elapsed interval, drying is applied first and the interval's rain
is added:

```
surface = min(2, surface * 2^(-elapsedHours / surfaceHalfLife) + rainInches / 0.10)
seepage = min(1, seepage * 2^(-elapsedHours / 72) + rainInches / 1.0)
```

A trace 0.01" model hour (0.1) stays Good, 0.05" reads wet, and 0.20" or more
reaches the cap, needing one extra half-life to clear compared with 0.10".
Seepage tracks multi-day wet spells: one 0.10" shower stays Good, while about
0.6" over two days reads likely. Seepage is enabled only for crags configured
to seep. Rain amounts are used
directly; precipitation probability does not scale the moisture input.

The surface reference half-life is `12 * (0.40 / DryingFactor)` hours, where
`DryingFactor` comes from `DryoutFor(rock)`. The
existing rock factors range from 0.20 for sandstone/conglomerate to 0.55 for
basalt/rhyolite/welded tuff, with granite/gneiss at 0.40 and limestone at 0.45.
They control drying speed; they are no longer rainfall thresholds.

Temperature and dew point give vapor pressure deficit (VPD): convert Fahrenheit
to Celsius, calculate `e(T) = 0.6108 * exp(17.27*T/(T+237.3))` in kPa, then use
`max(0, e(temperature) - e(dewPoint))`.

```
strength = 0.25 + clamp(VPD / 0.6, 0, 2)
                  * (0.5 + 0.5 * clamp(windMph / 10, 0, 1))
                  * (0.5 + clamp(exposedRadiation / 400, 0, 1))
surfaceHalfLife = clamp(referenceHalfLife / strength, 3, 48)
```

Endpoint temperature, dew point and wind are averaged for each interval.
Open-Meteo precipitation is the preceding-hour sum, and shortwave radiation is
the preceding-hour mean in W/m². Both are aligned to their interval endpoint;
aspect is evaluated at its midpoint. Solar exposure factors are shade 0,
mixed 0.3, all-day sun 1, and AM/PM sun 1 in its matching half (before/after
1pm), otherwise 0.1. Zero or missing radiation gives no solar benefit; cloud
cover does not invent sunshine at night. Missing RH does not prevent drying
estimation because temperature and dew point supply VPD.

Elapsed hours, rather than local clock differences, handle DST. The scraper
requires complete seven-day warmup coverage and all forecast windows before
replacing any region pages. Missing required weather fails the refresh;
optional radiation, RH, probability and AQI remain unavailable when absent.

## Ratings and explanations

Each state grades Good below 0.25, Marginal from 0.25, and Poor from 0.50.
Surface warnings read “drying” or “wet likely”; seepage warnings read “seepage
possible” or “seepage likely”. Each hour combines those estimates with the
existing temperature, dew point, wind and AQI rules.

A six-hour AM (7am–1pm) or PM (1pm–7pm) window is Good with four consecutive
Good hours and no Poor hours; Poor with at least four Poor hours; otherwise
Marginal. Forecast rain retains its existing whole-window amount/probability
floor. Historical totals impose no window floor. Equal severities prefer AQI,
forecast rain, seepage, surface wetness, temperature, dew point, then wind.
The hourly duration policy also summarizes the individual moisture detail rows.
When alternating conditions jointly limit a window, its selected moisture
warning remains visible in the corresponding row and wetness summary.

The worse half determines a crag's daily rating. An area is Good when more
than half its crags are Good; Poor when a majority are Poor and none Good;
otherwise Marginal. Shared results drive colors, icons, accessible labels and
Conditions explanations. Past precipitation over 24h and 72h and relative
humidity are neutral context. There is no displayed seven-day rain total.

The model responds to rain timing and drying opportunity instead of abruptly
forgetting rain at the 24h/72h boundary. Its coefficients and rock factors need
comparison with observed crag conditions before claiming predictive accuracy.

Data semantics: [Open-Meteo documentation](https://open-meteo.com/en/docs).
