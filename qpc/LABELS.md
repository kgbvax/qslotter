Decide how this station wants to receive a paper QSL card, using only what its QRZ record and bio say. Give a route label and, if the card must be sent via another callsign, that callsign (via).

Route labels:
- bureau: via the QSL bureau ("buro", "Büro", "bureau", "QSL via buro").
- direct: by post ("direct", "direkt", "SAE", "SASE", "IRC", "green stamps").
- oqrs: requested through an online QSL request service ("OQRS", Club Log OQRS, a web request form).
- unclear: a via callsign is named but the record does not say which route to use with it.
- no-paper: the station explicitly refuses paper cards ("NO QSL", "no paper", "LoTW only", "eQSL only", "no bureau, no direct").
- unknown: the record says nothing about paper QSL cards.

Via:
- The callsign the card is routed via: a QSL manager ("QSL via EA5GL", "QSL manager: IK2DUW", a callsign in the qslmgr field) or the operator's home call named for a portable or special call ("EA8/DL1ABC: QSL via DL1ABC").
- Empty when the card goes to the station itself. The station's own callsign is never a via.
- Uppercase callsign only, no extra words.

Rules:
1. Only the QRZ data counts, not outside knowledge.
2. Paper cards accepted but no route given ("mqsl = 1", "QSL welcome", "QSL OK", "100% QSL"), and no via callsign: bureau.
3. A via callsign with no route stated: unclear. If a route is stated for the via ("QSL via IK2DUW direct only"), use that route.
4. Several routes accepted and no preference stated: the cheapest, in the order bureau, oqrs, direct. A stated preference ("direct preferred", "bureau only", "no bureau") wins over this order.
5. no-paper needs an explicit refusal of paper. mqsl = 0 together with lotw = 1 or eqsl = 1 is not enough on its own: unknown.
6. LoTW, eQSL, Club Log matching or QRZ logbook confirmations are not paper routes. Mentioned alone they mean unknown.
7. The qslmgr field may hold free text instead of a callsign ("VIA BUREAU", "DIRECT ONLY"); read it like the bio.
8. The bio may be written in any language.

QRZ fields:
- qslmgr: QSL manager or QSL instructions, free text.
- mqsl: 1 = will return paper QSL, 0 = will not, empty = not stated.
- eqsl: 1 = accepts eQSL. lotw: 1 = uses LoTW. Neither is a paper route.
