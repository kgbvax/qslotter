package qsldetermine

// The words the assessment looks for. One table per idea, one entry per word
// and language: adding a language is adding words here, not changing logic.
// All entries are lower case; the tokenizer lower-cases the text first.

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

var (
	// routes
	bureauWords = set("bureau", "bureaux", "buro", "buero", "büro", "buró", "bureau's", "бюро", "byuro", "biuro")
	directWords = set("direct", "directly", "direkt", "direkte", "direkten", "directo", "directa", "directamente",
		"diretto", "diretta", "direttamente", "direto", "directement", "directe")

	// negation and "only"
	negators = set("no", "not", "never", "without", "nor", "kein", "keine", "keinen", "keiner", "nicht", "ohne", "nein",
		"non", "senza", "sin", "pas", "sans", "ni", "nie", "нет")
	onlyWords = set("only", "solely", "exclusively", "nur", "ausschließlich", "ausschliesslich", "ausschließlich",
		"solo", "sólo", "soltanto", "solamente", "seulement", "uniquement", "tylko")

	// negation passes through these when it looks back from a route word
	// ("no QSL via bureau", "no direct or bureau"); "and" does not distribute it
	negFillers = set("qsl", "qsls", "card", "cards", "via", "by", "per", "über", "ueber", "the", "a", "an", "any", "please",
		"your", "my", "to", "send", "sending", "mail", "paper", "karte", "karten", "or", "oder", "nor", "ni")

	// auxiliaries that allow a negator AFTER the route word ("direct is not accepted")
	auxWords = set("is", "are", "was", "be", "will", "does", "do", "wird", "werden", "ist", "sind", "can", "cannot", "accepted",
		"qsl", "card", "cards")

	// what a refusal can be about: "no QSL", "no paper", "keine Karten"
	qslWords = set("qsl", "qsls", "card", "cards", "paper", "papier", "karte", "karten", "qsl-karte", "tarjeta", "tarjetas",
		"cartolina", "cartoline", "carte", "cartes", "cartacea", "cartaceo")
	refusalSkip = set("any", "please", "printed", "more", "longer", "paper", "physical", "de", "di")
	// after "no QSL" these words make it a negated route, not a refusal of paper
	routeFollow = set("via", "by", "per", "über", "ueber", "manager", "mgr", "par", "por", "tramite")

	// something a bio sentence must contain before its route words count
	routeContext = set("qsl", "qsls", "card", "cards", "karte", "karten", "tarjeta", "tarjetas", "cartolina", "cartoline",
		"carte", "cartes", "paper", "papier", "mail", "send", "sent", "accept", "accepted", "accepts", "welcome", "reply",
		"replied", "via", "über", "ueber", "per", "tramite", "par", "por")
	// the stricter context for "eQSL only" in a bio
	qslContext = set("qsl", "qsls", "card", "cards", "karte", "karten", "tarjeta", "tarjetas", "cartolina", "cartoline",
		"carte", "cartes", "paper", "papier", "confirm", "confirms", "confirmed", "confirmation", "confirmations")

	// verbs that make "QSL card" in a bio a statement of acceptance
	acceptVerbs = set("send", "sent", "sending", "accept", "accepted", "accepts", "welcome", "welcomed", "reply", "replied",
		"replies", "return", "returned", "answer", "answered", "appreciated", "prefer", "preferred", "ok", "okay", "gladly",
		"happily", "gerne", "willkommen", "erwünscht", "erwuenscht")
	// money / postage words: a stated willingness to deal in paper
	costWords = set("sae", "irc", "stamp", "stamps", "usd", "eur", "euro", "euros", "dollar", "dollars", "postage")

	// words that limit a statement to a case ("No paper QSL for FT8")
	scopeWords = set("for", "when", "during", "if", "wenn", "falls", "bei", "für", "fuer", "pour", "quand", "para", "cuando",
		"except", "außer", "ausser")

	// electronic confirmation services (tokens after "e-qsl" is merged to "eqsl")
	electronicWords = map[string]string{
		"eqsl": "eQSL", "lotw": "LoTW", "clublog": "Clublog", "dcl": "DCL", "hrdlog": "HRDLog",
	}
	// the same, plus names that only mean "electronic" inside the short qslmgr field
	electronicFieldWords = map[string]string{
		"eqsl": "eQSL", "lotw": "LoTW", "clublog": "Clublog", "dcl": "DCL", "hrdlog": "HRDLog", "qrz": "QRZ",
	}
	electronicGeneric = set("electronic", "electronically", "digital", "elektronisch", "online")

	// values of the qslmgr field that mean "nothing here"
	emptyFieldValues = set("none", "n/a", "na", "nil", "-", "--", "?", "no", "nope", "keine", "kein", "null")
)
