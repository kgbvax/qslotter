Decide how this station wants to receive a paper QSL card, using only what its QRZ record and bio say. Give a route label and, if the card must be sent via another callsign, that callsign (via).

Route labels:
- bureau: via the QSL bureau ("buro", "Büro", "bureau", "QSL via buro").
- direct: by post to the station's postal address ("direct", "direkt", "SAE", "SASE", "IRC", "green stamps"). Needs a full postal address, see rule 5.
- oqrs: requested through an online QSL request service ("OQRS", Club Log OQRS, a web request form).
- unclear: the record asks for paper cards but the route cannot be used as stated: a via callsign with no route, or direct as the only route but no postal address.
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
5. Direct needs a full postal address (at least street and city), in the QRZ postal address or written in the bio. Without one, use the next route the record accepts (bureau, oqrs); if direct is the only route: unclear. This rule is about cards to the station itself: for a via callsign the address is on that callsign's own record, so it does not apply.
6. A postal address alone, with nothing else about QSL cards, is not a route: unknown.
7. no-paper needs an explicit refusal of paper. mqsl = 0 together with lotw = 1 or eqsl = 1 is not enough on its own: unknown.
8. LoTW, eQSL, Club Log matching or QRZ logbook confirmations are not paper routes. Mentioned alone they mean unknown.
9. The qslmgr field may hold free text instead of a callsign ("VIA BUREAU", "DIRECT ONLY"); read it like the bio.
10. The bio may be written in any language.

QRZ fields:
- qslmgr: QSL manager or QSL instructions, free text.
- mqsl: 1 = will return paper QSL, 0 = will not, empty = not stated.
- eqsl: 1 = accepts eQSL. lotw: 1 = uses LoTW. Neither is a paper route.
- postal address: street, city, state, zip and country as published on QRZ. Owners may leave parts empty or hide it.
