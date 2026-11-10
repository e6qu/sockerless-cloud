package main

import (
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// CloudWatch Logs' managed data identifiers, as the data protection guide's
// "Types of data that you can protect" pages list them: each one's keywords,
// countries and notes. AWS publishes no pattern of its own, so each detector
// matches the published format of its data type, applies the checksum the
// type defines where it has one, and requires one of the guide's keywords
// within 30 characters of the data where the guide requires a keyword.

const (
	cwDataIdentifierArnPrefix = "arn:aws:dataprotection::aws:data-identifier/"
	cwKeywordProximity        = 30
)

type cwDataDetector struct {
	pattern *regexp.Regexp
	// keywords, when set, must appear within 30 characters of a match.
	keywords *regexp.Regexp
	// keywordOptional reports a match that needs no keyword, such as a phone
	// number carrying its country code.
	keywordOptional func(match string) bool
	valid           func(match string) bool
}

type cwDataSpan struct{ start, end int }

// find returns the byte spans of message the detector matches; the pattern's
// first group, when it has one, is the data.
func (d cwDataDetector) find(message string) []cwDataSpan {
	var out []cwDataSpan
	for _, loc := range d.pattern.FindAllStringSubmatchIndex(message, -1) {
		start, end := loc[0], loc[1]
		if len(loc) >= 4 && loc[2] >= 0 {
			start, end = loc[2], loc[3]
		}
		match := message[start:end]
		if d.valid != nil && !d.valid(match) {
			continue
		}
		if d.keywords != nil && (d.keywordOptional == nil || !d.keywordOptional(match)) && !cwKeywordNear(d.keywords, message, start, end) {
			continue
		}
		out = append(out, cwDataSpan{start, end})
	}
	return out
}

// cwKeywordNear reports a keyword match ending at most 30 characters before
// start or beginning at most 30 characters after end.
func cwKeywordNear(keywords *regexp.Regexp, message string, start, end int) bool {
	from := cwBackRunes(message, start, cwKeywordProximity+64)
	to := cwForwardRunes(message, end, cwKeywordProximity+64)
	window := message[from:to]
	for _, loc := range keywords.FindAllStringIndex(window, -1) {
		kStart, kEnd := from+loc[0], from+loc[1]
		if kEnd <= start && cwRuneCount(message[kEnd:start]) <= cwKeywordProximity {
			return true
		}
		if kStart >= end && cwRuneCount(message[end:kStart]) <= cwKeywordProximity {
			return true
		}
		if kStart < end && kEnd > start {
			return true
		}
	}
	return false
}

func cwRuneCount(s string) int { return len([]rune(s)) }

func cwBackRunes(s string, at, n int) int {
	for ; n > 0 && at > 0; n-- {
		at--
		for at > 0 && !utf8RuneStart(s[at]) {
			at--
		}
	}
	return at
}

func cwForwardRunes(s string, at, n int) int {
	for ; n > 0 && at < len(s); n-- {
		at++
		for at < len(s) && !utf8RuneStart(s[at]) {
			at++
		}
	}
	return at
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// cwKeywordPattern compiles keywords case-insensitively. A keyword with a
// space also matches without it or with an underscore or hyphen in its place,
// as the guide describes; a keyword that starts or ends with a letter or
// digit does not match inside a longer word.
func cwKeywordPattern(keywords ...string) *regexp.Regexp {
	alts := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		parts := strings.Split(kw, " ")
		for i, p := range parts {
			parts[i] = regexp.QuoteMeta(p)
		}
		alt := strings.Join(parts, `[ _-]?`)
		runes := []rune(kw)
		if unicode.IsLetter(runes[0]) || unicode.IsDigit(runes[0]) {
			alt = `(?:^|[^\pL\pN])` + alt
		}
		if last := runes[len(runes)-1]; unicode.IsLetter(last) || unicode.IsDigit(last) {
			alt += `(?:[^\pL\pN]|$)`
		}
		alts = append(alts, alt)
	}
	return regexp.MustCompile(`(?i)(?:` + strings.Join(alts, "|") + `)`)
}

func cwDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func cwLuhn(digits string) bool {
	sum, double := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return len(digits) > 0 && sum%10 == 0
}

// The sequences card issuers reserve for public testing, which the financial
// information page says CloudWatch Logs does not report.
var cwTestCardNumbers = map[string]bool{}

func init() {
	for _, n := range strings.Fields(`122000000000003 2222405343248877 2222990905257051 2223007648726984 2223577120017656
30569309025904 34343434343434 3528000700000000 3530111333300000 3566002020360505 36148900647913
36700102000000 371449635398431 378282246310005 378734493671000 38520000023237 4012888888881881
4111111111111111 4222222222222 4444333322221111 4462030000000000 4484070000000000 4911830000000
4917300800000000 4917610000000000 4917610000000000003 5019717010103742 5105105105105100
5111010030175156 5185540810000019 5200828282828210 5204230080000017 5204740009900014 5420923878724339
5454545454545454 5455330760000018 5506900490000436 5506900490000444 5506900510000234 5506920809243667
5506922400634930 5506927427317625 5553042241984105 5555553753048194 5555555555554444 5610591081018250
6011000990139424 6011000400000000 6011111111111117 630490017740292441 630495060000000000
6331101999990016 6759649826438453 6799990100000000019 76009244561`) {
		cwTestCardNumbers[n] = true
	}
}

// The card number prefixes of American Express, Dankort, Diners Club,
// Discover, Electron, JCB, Mastercard, UnionPay and Visa.
var cwCardPrefix = regexp.MustCompile(`^(?:3[47]|5019|30[0-5]|36|38|39|6011|64[4-9]|65|35(?:2[89]|[3-8]\d)|5[1-5]|2(?:2[2-9]|[3-6]\d|7[01]|720)|62|4)`)

func cwValidCard(match string) bool {
	d := cwDigits(match)
	return len(d) >= 13 && len(d) <= 19 && cwLuhn(d) && cwCardPrefix.MatchString(d) && !cwTestCardNumbers[d]
}

// An IBAN's check digits make the number, moved and lettered as ISO 13616
// describes, 1 modulo 97.
func cwValidIBAN(match string) bool {
	s := strings.ToUpper(strings.ReplaceAll(match, " ", ""))
	if len(s) < 15 {
		return false
	}
	s = s[4:] + s[:4]
	rem := 0
	for _, r := range s {
		var v int
		switch {
		case r >= '0' && r <= '9':
			v = int(r - '0')
			rem = (rem*10 + v) % 97
		case r >= 'A' && r <= 'Z':
			v = int(r-'A') + 10
			rem = (rem*100 + v) % 97
		default:
			return false
		}
	}
	return rem == 1
}

// A DEA registration number's last digit is the last digit of the sum of its
// first, third and fifth digits and twice its second, fourth and sixth.
func cwValidDEA(match string) bool {
	d := cwDigits(match)
	if len(d) != 7 {
		return false
	}
	n := func(i int) int { return int(d[i] - '0') }
	return (n(0)+n(2)+n(4)+2*(n(1)+n(3)+n(5)))%10 == n(6)
}

// A National Provider Identifier passes the Luhn check prefixed with 80840.
func cwValidNPI(match string) bool {
	d := cwDigits(match)
	return len(d) == 10 && cwLuhn("80840"+d)
}

// An NHS number's tenth digit is its modulus 11 check digit.
func cwValidNHS(match string) bool {
	d := cwDigits(match)
	if len(d) != 10 {
		return false
	}
	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(d[i]-'0') * (10 - i)
	}
	check := 11 - sum%11
	if check == 11 {
		check = 0
	}
	return check != 10 && check == int(d[9]-'0')
}

func cwValidCPF(match string) bool {
	d := cwDigits(match)
	if len(d) != 11 || strings.Count(d, d[:1]) == 11 {
		return false
	}
	check := func(n int) int {
		sum := 0
		for i := 0; i < n; i++ {
			sum += int(d[i]-'0') * (n + 1 - i)
		}
		r := sum * 10 % 11
		if r == 10 {
			r = 0
		}
		return r
	}
	return check(9) == int(d[9]-'0') && check(10) == int(d[10]-'0')
}

func cwValidCNPJ(match string) bool {
	d := cwDigits(match)
	if len(d) != 14 || strings.Count(d, d[:1]) == 14 {
		return false
	}
	check := func(weights []int) int {
		sum := 0
		for i, w := range weights {
			sum += int(d[i]-'0') * w
		}
		r := sum % 11
		if r < 2 {
			return 0
		}
		return 11 - r
	}
	return check([]int{5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}) == int(d[12]-'0') &&
		check([]int{6, 5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}) == int(d[13]-'0')
}

// A Spanish DNI, NIE or NIF letter is the number modulo 23 in this table, an
// NIE's leading X, Y or Z counting as 0, 1 or 2.
func cwValidSpanishID(match string) bool {
	s := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(match))
	if len(s) != 9 {
		return false
	}
	lead := map[byte]string{'X': "0", 'Y': "1", 'Z': "2", 'K': "", 'L': "", 'M': ""}
	body := s[:8]
	if p, ok := lead[body[0]]; ok {
		if p == "" {
			return true
		}
		body = p + body[1:]
	}
	n, err := strconv.Atoi(body)
	return err == nil && "TRWAGMYFPDXBNJZSQVHLCKE"[n%23] == s[8]
}

func cwValidSSN(match string) bool {
	d := cwDigits(match)
	if len(d) != 9 {
		return false
	}
	area, group, serial := d[:3], d[3:5], d[5:]
	return area != "000" && area != "666" && area[0] != '9' && group != "00" && serial != "0000"
}

func cwValidIP(match string) bool {
	return strings.IndexFunc(match, func(r rune) bool { return r != ':' && r != '.' }) >= 0 && net.ParseIP(match) != nil
}

func cwHasLetterAndDigit(match string) bool {
	return strings.IndexFunc(match, unicode.IsLetter) >= 0 && strings.IndexFunc(match, unicode.IsDigit) >= 0
}

func cwHasDigit(match string) bool { return strings.IndexFunc(match, unicode.IsDigit) >= 0 }

func cwStartsWithCountryCode(codes ...string) func(string) bool {
	return func(match string) bool {
		m := strings.TrimSpace(match)
		for _, c := range codes {
			if strings.HasPrefix(m, "+"+c) || strings.HasPrefix(m, "00"+c) {
				return true
			}
		}
		return false
	}
}

// Common given names of Latin script, the first word a full name the Name
// identifier detects begins with.
var cwGivenNames = map[string]bool{}

func init() {
	for _, n := range strings.Fields(`aaron abigail adam adrian aisha alan albert alejandro alex alexander alexandra alice alicia amanda amelia amy ana andrea andrew angela anna anne anthony antonio arthur ashley barbara ben benjamin beth betty bianca brandon brenda brian bruce bruno carl carla carlos carmen carol caroline catherine charles charlotte chen chris christian christina christine christopher claire claudia daniel daniela david deborah dennis diana diego donald donna dorothy douglas edward elena elizabeth emily emma eric eva fatima felipe fernando francesca francisco frank gabriel gary george giovanni giulia grace gregory hannah hans harry heather helen henry hugo ian isabel isabella jack jacob james jane janet jason javier jean jeffrey jennifer jessica jim joan joe john jonathan jorge jose joseph joshua juan julia julie justin karen katherine kelly kenneth kevin kim kimberly klaus laura lauren leonardo linda lisa lucas lucia luis luke marco margaret maria marie mario mark martha martin mary matthew maria michael michelle miguel mohammed nancy natalie nathan nicholas nicole noah olivia oscar pablo pamela patricia patrick paul pedro peter rachel raymond rebecca richard robert roberto ronald rose ruth ryan samantha samuel sandra sara sarah scott sean sebastian sharon sophia sophie stephanie stephen steven susan teresa thomas timothy tyler victoria vincent walter wei william zachary`) {
		cwGivenNames[n] = true
	}
}

func cwValidName(match string) bool {
	first, _, _ := strings.Cut(match, " ")
	return cwGivenNames[strings.ToLower(first)]
}

var cwDriversLicenseKeywords = map[string][]string{
	"AT": {"führerschein", "fuhrerschein", "führerschein republik österreich", "fuhrerschein republik osterreich"},
	"AU": {"dl#", "dl:", "dl :", "dlno#", "driver licence", "driver license", "driver permit", "drivers lic.", "drivers licence", "driver's licence", "drivers license", "driver's license", "drivers permit", "driver's permit", "drivers permit number", "driving licence", "driving license", "driving permit"},
	"BE": {"fuehrerschein", "fuehrerschein- nr", "fuehrerscheinnummer", "fuhrerschein", "führerschein", "fuhrerschein- nr", "führerschein- nr", "fuhrerscheinnummer", "führerscheinnummer", "numéro permis conduire", "permis de conduire", "rijbewijs", "rijbewijsnummer"},
	"BG": {"превозно средство", "свидетелство за управление на моторно", "свидетелство за управление на мпс", "сумпс", "шофьорска книжка"},
	"CA": {"dl#", "dl:", "dlno#", "driver licence", "driver licences", "driver license", "driver licenses", "driver permit", "drivers lic.", "drivers licence", "driver's licence", "drivers licences", "driver's licences", "drivers license", "driver's license", "drivers licenses", "driver's licenses", "drivers permit", "driver's permit", "drivers permit number", "driving licence", "driving license", "driving permit", "permis de conduire"},
	"CY": {"άδεια οδήγησης"},
	"CZ": {"číslo licence", "císlo licence řidiče", "číslo řidičského průkazu", "ovladače lic.", "povolení k jízdě", "povolení řidiče", "řidiči povolení", "řidičský prúkaz", "řidičský průkaz"},
	"DE": {"fuehrerschein", "fuehrerschein- nr", "fuehrerscheinnummer", "fuhrerschein", "führerschein", "fuhrerschein- nr", "führerschein- nr", "fuhrerscheinnummer", "führerscheinnummer"},
	"DK": {"kørekort", "kørekortnummer"},
	"EE": {"juhi litsentsi number", "juhiloa number", "juhiluba", "juhiluba number"},
	"ES": {"carnet conducer", "el carnet de conducer", "licencia conducer", "licencia de manejo", "número carnet conducer", "número de carnet de conducer", "número de permiso conducer", "número de permiso de conducer", "número licencia conducer", "número permiso conducer", "permiso conducción", "permiso conducer", "permiso de conducción"},
	"FI": {"ajokortin numero", "ajokortti", "förare lic.", "körkort", "körkort nummer", "kuljettaja lic.", "permis de conduire"},
	"FR": {"permis de conduire"},
	"GB": {"dl#", "dl:", "dlno#", "driver licence", "driver licences", "driver license", "driver licenses", "driver permit", "drivers lic.", "drivers licence", "driver's licence", "drivers licences", "driver's licences", "drivers license", "driver's license", "drivers licenses", "driver's licenses", "drivers permit", "driver's permit", "drivers permit number", "driving licence", "driving license", "driving permit"},
	"GR": {"δεια οδήγησης", "adeia odigisis"},
	"HR": {"vozačka dozvola"},
	"HU": {"illesztőprogramok lic", "jogosítvány", "jogsi", "licencszám", "vezető engedély", "vezetői engedély"},
	"IE": {"ceadúnas tiomána"},
	"IT": {"patente di guida", "patente di guida numero", "patente guida", "patente guida numero"},
	"LT": {"vairuotojo pažymėjimas"},
	"LU": {"fahrerlaubnis", "führerschäin"},
	"LV": {"autovadītāja apliecība", "licences numurs", "vadītāja apliecība", "vadītāja apliecības numurs", "vadītāja atļauja", "vadītāja licences numurs", "vadītāji lic."},
	"MT": {"liċenzja tas-sewqan"},
	"NL": {"permis de conduire", "rijbewijs", "rijbewijsnummer"},
	"PL": {"numer licencyjny", "prawo jazdy", "zezwolenie na prowadzenie"},
	"PT": {"carta de condução", "carteira de habilitação", "carteira de motorist", "carteira habilitação", "carteira motorist", "licença condução", "licença de condução", "número de licença", "número licença", "permissão condução", "permissão de condução"},
	"RO": {"numărul permisului de conducere", "permis de conducere"},
	"SE": {"ajokortin numero", "dlno#", "ajokortti", "drivere lic.", "förare lic.", "körkort", "körkort nummer", "körkortsnummer", "kuljettajat lic."},
	"SI": {"vozniško dovoljenje"},
	"SK": {"číslo licencie", "číslo vodičského preukazu", "ovládače lic.", "povolenia vodičov", "povolenie jazdu", "povolenie na jazdu", "povolenie vodiča", "vodičský preukaz"},
	"US": {"dl#", "dl:", "dlno#", "driver licence", "driver licences", "driver license", "driver licenses", "driver permit", "drivers lic.", "drivers licence", "driver's licence", "drivers licences", "driver's licences", "drivers license", "driver's license", "drivers licenses", "driver's licenses", "drivers permit", "driver's permit", "drivers permit number", "driving licence", "driving license", "driving permit"},
}

var cwBankAccountKeywords = map[string][]string{
	"FR": {"account code", "account number", "accountno#", "accountnumber#", "bban", "code bancaire", "compte bancaire", "customer account id", "customer account number", "customer bank account id", "iban", "numéro de compte"},
	"DE": {"account code", "account number", "accountno#", "accountnumber#", "bankleitzahl", "bban", "customer account id", "customer account number", "customer bank account id", "geheimzahl", "iban", "kartennummer", "kontonummer", "kreditkartennummer", "sepa"},
	"IT": {"account code", "account number", "accountno#", "accountnumber#", "bban", "codice bancario", "conto bancario", "customer account id", "customer account number", "customer bank account id", "iban", "numero di conto"},
	"ES": {"account code", "account number", "accountno#", "accountnumber#", "bban", "código cuenta", "código cuenta bancaria", "cuenta cliente id", "customer account id", "customer account number", "customer bank account id", "iban", "número cuenta bancaria cliente", "número cuenta cliente"},
	"GB": {"account code", "account number", "accountno#", "accountnumber#", "bban", "customer account id", "customer account number", "customer bank account id", "iban", "sepa"},
	"US": {"bank account", "bank acct", "checking account", "checking acct", "deposit account", "deposit acct", "savings account", "savings acct", "chequing account", "chequing acct"},
}

var cwIBANPatterns = map[string]string{
	"DE": `DE\d{2}(?: ?\d{4}){4} ?\d{2}`,
	"ES": `ES\d{2}(?: ?\d{4}){5}`,
	"FR": `FR\d{2}(?: ?\d{4}){2} ?\d{2}[0-9A-Z]{2}(?: ?[0-9A-Z]{4}){2} ?[0-9A-Z]\d{2}`,
	"GB": `GB\d{2} ?[A-Z]{4}(?: ?\d{4}){3} ?\d{2}`,
	"IT": `IT\d{2} ?[A-Z]\d{3}(?: ?\d{4}) ?\d{3}[0-9A-Z](?: ?[0-9A-Z]{4}){2} ?[0-9A-Z]{3}`,
}

var cwPassportPatterns = map[string]struct {
	pattern  string
	keywords []string
}{
	"CA": {`[A-Za-z]{2}\d{6}`, []string{"passeport", "passeport#", "passport", "passport#", "passportno", "passportno#"}},
	"DE": {`[CFGHJKcfghjk][CFGHJKLMNPRTVWXYZcfghjklmnprtvwxyz0-9]{8}`, []string{"ausstellungsdatum", "ausstellungsort", "geburtsdatum", "passport", "passports", "reisepass", "reisepass–nr", "reisepassnummer"}},
	"ES": {`[A-Za-z]{2,3}\d{6}`, []string{"españa pasaporte", "libreta pasaporte", "número pasaporte", "pasaporte", "passport", "passport book", "passport no", "passport number", "spain passport"}},
	"FR": {`\d{2}[A-Za-z]{2}\d{5}`, []string{"numéro de passeport", "passeport", "passeport#", "passeport #", "passeportn °", "passeport n °", "passeportnon", "passeport non"}},
	"GB": {`\d{9}`, []string{"passeport #", "passeport n °", "passeportnon", "passeport non", "passeportn °", "passport #", "passport no", "passport number", "passport#", "passportid"}},
	"IT": {`[A-Za-z]{2}\d{7}`, []string{"italian passport number", "numéro passeport", "numéro passeport italien", "passaporto", "passaporto italiana", "passaporto numero", "passport number", "repubblica italiana passaporto"}},
	"US": {`[A-Za-z]?\d{8,9}`, []string{"passport", "travel document"}},
}

var cwPhonePatterns = map[string]struct {
	pattern string
	codes   []string
}{
	"BR": {`(?:(?:\+|00)55[ .-]?)?\(?\d{2}\)?[ .-]?9?\d{4}[ .-]?\d{4}`, []string{"55"}},
	"DE": {`(?:(?:\+|00)49[ .-]?|0)\(?\d{2,5}\)?[ /.-]?\d{3,8}`, []string{"49"}},
	"ES": {`(?:(?:\+|00)34[ .-]?)?[6789]\d{2}[ .-]?\d{3}[ .-]?\d{3}`, []string{"34"}},
	"FR": {`(?:(?:\+|00)33[ .-]?|0)[1-9](?:[ .-]?\d{2}){4}`, []string{"33"}},
	"GB": {`(?:(?:\+|00)44[ .-]?|0)\d{2,4}[ .-]?\d{3,4}[ .-]?\d{3,4}`, []string{"44"}},
	"IT": {`(?:(?:\+|00)39[ .-]?)?(?:0\d{1,3}|3\d{2})[ .-]?\d{6,8}`, []string{"39"}},
	"US": {`(?:(?:\+|00)1[ .-]?)?\(?[2-9]\d{2}\)?[ .-]?[2-9]\d{2}[ .-]?\d{4}`, []string{"1"}},
}

var cwTaxIdentifiers = map[string]struct {
	pattern  string
	keywords []string
}{
	"DE": {`\d{11}|\d{2,3}/\d{3,4}/\d{4,5}`, []string{"identifikationsnummer", "steuer id", "steueridentifikationsnummer", "steuernummer", "tax id", "tax identification number", "tax number"}},
	"ES": {`[ABCDEFGHJNPQRSUVWabcdefghjnpqrsuvw]-?\d{7}-?[0-9A-Ja-j]`, cwSpanishTaxKeywords},
	"FR": {`[0-3]\d{12}|[0-3]\d \d{2} \d{3} \d{3} \d{3}`, []string{"numéro d'identification fiscale", "tax id", "tax identification number", "tax number", "tin", "tin#"}},
	"GB": {`\d{10}|\d{2}[A-Za-z]\d{5}`, []string{"paye", "tax id", "tax id no.", "tax id number", "tax identification", "tax identification#", "tax no.", "tax number", "tax reference", "tax#", "taxid#", "temporary reference number", "tin", "trn", "unique tax reference", "unique taxpayer reference", "utr"}},
}

var cwSpanishTaxKeywords = []string{"cif", "cif número", "cifnúmero#", "nie", "nif", "número de contribuyente", "número de identidad de extranjero", "número de identificación fiscal", "número de impuesto corporativo", "personal tax number", "tax id", "tax identification number", "tax number", "tin", "tin#"}

var cwBrazilTaxKeywords = []string{"cadastro de pessoa física", "cadastro de pessoa fisica", "cadastro de pessoas físicas", "cadastro de pessoas fisicas", "cadastro nacional da pessoa jurídica", "cadastro nacional da pessoa juridica", "cnpj", "cpf"}

var cwFranceNationalIDKeywords = []string{"assurance sociale", "code sécurité sociale", "french social security number", "fssn#", "insee", "insurance number", "national id number", "nationalid#", "numéro d'assurance", "sécurité sociale", "sécurité sociale non.", "sécurité sociale numéro", "social", "social security", "social security number", "socialsecuritynumber", "ss#", "ssn", "ssn#"}

const cwFranceNIR = `[12] ?\d{2} ?(?:0[1-9]|1[0-2]) ?(?:\d{2}|2[ABab]) ?\d{3} ?\d{3} ?\d{2}`

func cwBounded(pattern string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^\pL\pN])(` + pattern + `)(?:[^\pL\pN]|$)`)
}

var cwManagedDataIdentifiers = map[string]cwDataDetector{}

func cwAddIdentifier(name string, d cwDataDetector) { cwManagedDataIdentifiers[name] = d }

func init() {
	kw := cwKeywordPattern

	cwAddIdentifier("AwsSecretKey", cwDataDetector{
		pattern:  regexp.MustCompile(`(?:^|[^A-Za-z0-9/+=])([A-Za-z0-9/+]{40})(?:[^A-Za-z0-9/+=]|$)`),
		keywords: kw("aws_secret_access_key", "credentials", "secret access key", "secret key", "set-awscredential"),
		valid:    cwHasLetterAndDigit,
	})
	cwAddIdentifier("OpenSshPrivateKey", cwDataDetector{pattern: regexp.MustCompile(`-----BEGIN OPENSSH PRIVATE KEY-----[\s\S]*?-----END OPENSSH PRIVATE KEY-----`)})
	cwAddIdentifier("PgpPrivateKey", cwDataDetector{pattern: regexp.MustCompile(`-----BEGIN PGP PRIVATE KEY BLOCK-----[\s\S]*?-----END PGP PRIVATE KEY BLOCK-----`)})
	cwAddIdentifier("PkcsPrivateKey", cwDataDetector{pattern: regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |ENCRYPTED )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |DSA |ENCRYPTED )?PRIVATE KEY-----`)})
	cwAddIdentifier("PuttyPrivateKey", cwDataDetector{pattern: regexp.MustCompile(`PuTTY-User-Key-File-[23]: [\s\S]*?Private-MAC: [0-9a-fA-F]+`)})

	cwAddIdentifier("IpAddress", cwDataDetector{
		pattern: cwBounded(`(?:\d{1,3}\.){3}\d{1,3}|(?:[0-9A-Fa-f]{0,4}:){2,7}[0-9A-Fa-f]{0,4}`),
		valid:   cwValidIP,
	})

	cwAddIdentifier("CreditCardNumber", cwDataDetector{
		pattern:  cwBounded(`\d(?:[ -]?\d){12,18}`),
		keywords: kw("account number", "american express", "amex", "bank card", "card", "card number", "card num", "cc #", "ccn", "check card", "credit", "credit card#", "dankort", "debit", "debit card", "diners club", "discover", "electron", "japanese card bureau", "jcb", "mastercard", "mc", "pan", "payment account number", "payment card number", "pcn", "union pay", "visa"),
		valid:    cwValidCard,
	})
	cwAddIdentifier("CreditCardExpiration", cwDataDetector{
		pattern:  cwBounded(`(?:0[1-9]|1[0-2]) ?[/-] ?(?:\d{4}|\d{2})`),
		keywords: kw("exp d", "exp m", "exp y", "expiration", "expiry"),
	})
	cwAddIdentifier("CreditCardSecurityCode", cwDataDetector{
		pattern:  cwBounded(`\d{3,4}`),
		keywords: kw("card id", "card identification code", "card identification number", "card security code", "card validation code", "card validation number", "card verification data", "card verification value", "cvc", "cvc2", "cvv", "cvv2", "elo verification code"),
	})
	for country, keywords := range cwBankAccountKeywords {
		if country == "US" {
			cwAddIdentifier("BankAccountNumber-US", cwDataDetector{pattern: cwBounded(`\d{8,17}`), keywords: kw(keywords...)})
			continue
		}
		cwAddIdentifier("BankAccountNumber-"+country, cwDataDetector{
			pattern:  cwBounded(cwIBANPatterns[country]),
			keywords: kw(keywords...),
			valid:    cwValidIBAN,
		})
	}

	cwAddIdentifier("DrugEnforcementAgencyNumber-US", cwDataDetector{
		pattern: cwBounded(`[A-Za-z][A-Za-z9]\d{7}`), keywords: kw("dea number", "dea registration"), valid: cwValidDEA,
	})
	cwAddIdentifier("HealthInsuranceCardNumber-EU", cwDataDetector{
		pattern:  cwBounded(`[0-9A-Za-z]{2,4}[0-9]{6,16}`),
		keywords: kw("assicurazione sanitaria numero", "carta assicurazione numero", "carte d’assurance maladie", "carte européenne d'assurance maladie", "ceam", "ehic", "ehic#", "finlandehicnumber#", "gesundheitskarte", "hälsokort", "health card", "health card number", "health insurance card", "health insurance number", "insurance card number", "krankenversicherungskarte", "krankenversicherungsnummer", "medical account number", "numero conto medico", "numéro d’assurance maladie", "numéro de carte d’assurance", "numéro de compte medical", "número de cuenta médica", "número de seguro de salud", "número de tarjeta de seguro", "sairaanhoitokortin", "sairausvakuutuskortti", "sairausvakuutusnumero", "sjukförsäkring nummer", "sjukförsäkringskort", "suomi ehic-numero", "tarjeta de salud", "terveyskortti", "tessera sanitaria assicurazione numero", "versicherungsnummer"),
	})
	cwAddIdentifier("HealthInsuranceClaimNumber-US", cwDataDetector{
		pattern:  cwBounded(`\d{3}-?\d{2}-?\d{4}[A-Za-z]\d?`),
		keywords: kw("health insurance claim number", "hic no", "hic no.", "hic number", "hic#", "hicn", "hicn#", "hicno#"),
	})
	cwAddIdentifier("HealthInsuranceNumber-FR", cwDataDetector{
		pattern: cwBounded(cwFranceNIR), keywords: kw("carte d'assuré social", "carte vitale", "insurance card"),
	})
	cwAddIdentifier("HealthcareProcedureCode-US", cwDataDetector{
		pattern: cwBounded(`[A-Va-v]\d{4}`), keywords: kw("current procedural terminology", "hcpcs", "healthcare common procedure coding system"),
	})
	const mbiLetter = `[AC-HJKMNP-RT-Yac-hjkmnp-rt-y]`
	cwAddIdentifier("MedicareBeneficiaryNumber-US", cwDataDetector{
		pattern:  cwBounded(`[1-9]` + mbiLetter + `[AC-HJKMNP-RT-Yac-hjkmnp-rt-y0-9]\d-?` + mbiLetter + `[AC-HJKMNP-RT-Yac-hjkmnp-rt-y0-9]\d-?` + mbiLetter + mbiLetter + `\d{2}`),
		keywords: kw("mbi", "medicare beneficiary"),
	})
	cwAddIdentifier("NationalDrugCode-US", cwDataDetector{
		pattern: cwBounded(`\d{4,5}-\d{3,4}-\d{1,2}`), keywords: kw("national drug code", "ndc"),
	})
	cwAddIdentifier("NationalProviderId-US", cwDataDetector{
		pattern: cwBounded(`\d{10}`), keywords: kw("hipaa", "n.p.i.", "national provider", "npi"), valid: cwValidNPI,
	})
	cwAddIdentifier("NhsNumber-GB", cwDataDetector{
		pattern: cwBounded(`\d{3}[ -]?\d{3}[ -]?\d{4}`), keywords: kw("national health service", "nhs"), valid: cwValidNHS,
	})
	cwAddIdentifier("PersonalHealthNumber-CA", cwDataDetector{
		pattern: cwBounded(`\d{4}[ -]?\d{3}[ -]?\d{3}`), keywords: kw("canada healthcare number", "msp number", "care number", "phn", "soins de santé"),
	})

	cwAddIdentifier("DateOfBirth", cwDataDetector{
		pattern:  cwBounded(`\d{4}[/-]\d{1,2}[/-]\d{1,2}|\d{1,2}[/ -]\d{1,2}[/ -]\d{2,4}|\d{1,2}[ -](?i:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*[ ,-]+\d{4}|(?i:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]* \d{1,2},? \d{4}`),
		keywords: kw("dob", "date of birth", "birthdate", "birth date", "birthday", "b-day", "bday"),
	})
	cwAddIdentifier("CepCode-BR", cwDataDetector{
		pattern: cwBounded(`\d{5}-?\d{3}`), keywords: kw("cep", "código de endereçamento postal", "codigo de endereçamento postal"),
	})
	cwAddIdentifier("Cnpj-BR", cwDataDetector{
		pattern:  cwBounded(`\d{2}\.?\d{3}\.?\d{3}/?\d{4}-?\d{2}`),
		keywords: kw(append([]string{"cadastro nacional da pessoa jurídica", "cadastro nacional da pessoa juridica", "cnpj"}, cwBrazilTaxKeywords...)...),
		valid:    cwValidCNPJ,
	})
	cwAddIdentifier("CpfCode-BR", cwDataDetector{
		pattern:  cwBounded(`\d{3}\.?\d{3}\.?\d{3}-?\d{2}`),
		keywords: kw(append([]string{"cadastro de pessoas fisicas", "cadastro de pessoas físicas", "cadastro de pessoa física", "cadastro de pessoa fisica", "cpf"}, cwBrazilTaxKeywords...)...),
		valid:    cwValidCPF,
	})
	for country, keywords := range cwDriversLicenseKeywords {
		cwAddIdentifier("DriversLicense-"+country, cwDataDetector{
			pattern:  cwBounded(`[A-Za-z]{0,3}\d[0-9A-Za-z-]{3,16}`),
			keywords: kw(keywords...),
			valid:    cwHasDigit,
		})
	}
	cwAddIdentifier("ElectoralRollNumber-GB", cwDataDetector{
		pattern:  cwBounded(`[A-Za-z]{2,3}\d{1,4}(?:-\d{1,4})?`),
		keywords: kw("electoral #", "electoral number", "electoral roll #", "electoral roll no.", "electoral roll number", "electoralrollno"),
	})
	cwAddIdentifier("IndividualTaxIdentificationNumber-US", cwDataDetector{
		pattern:  cwBounded(`9\d{2}[- ]?(?:5\d|6[0-5]|7\d|8[0-8]|9[0-2]|9[4-9])[- ]?\d{4}`),
		keywords: kw("individual taxpayer identification number", "itin", "i.t.i.n."),
	})
	cwAddIdentifier("InseeCode-FR", cwDataDetector{pattern: cwBounded(cwFranceNIR), keywords: kw(cwFranceNationalIDKeywords...)})
	cwAddIdentifier("NationalIdentificationNumber-DE", cwDataDetector{
		pattern:  cwBounded(`[CFGHJKLMNPRTVWXYZcfghjklmnprtvwxyz][CFGHJKLMNPRTVWXYZcfghjklmnprtvwxyz0-9]{8}\d?`),
		keywords: kw("ausweisnummer", "id number", "identification number", "identity number", "insurance number", "personal id", "personalausweis"),
		valid:    cwHasDigit,
	})
	cwAddIdentifier("NationalIdentificationNumber-ES", cwDataDetector{
		pattern:  cwBounded(`\d{8}-?[A-Za-z]`),
		keywords: kw("dni", "dni#", "dninúmero#", "documento nacional de identidad", "identidad único", "identidadúnico#", "insurance number", "national identification number", "national identity", "nationalid#", "nationalidno#", "número nacional identidad", "personal identification number", "personal identity no", "unique identity number", "uniqueid#"),
		valid:    cwValidSpanishID,
	})
	cwAddIdentifier("NationalIdentificationNumber-IT", cwDataDetector{
		pattern:  cwBounded(`[A-Za-z]{6}\d{2}[A-Za-z]\d{2}[A-Za-z]\d{3}[A-Za-z]`),
		keywords: kw("codice fiscal", "dati anagrafici", "ehic", "health card", "health insurance card", "p. iva", "partita i.v.a.", "personal data", "tax code", "tessera sanitaria"),
	})
	cwAddIdentifier("NationalInsuranceNumber-GB", cwDataDetector{
		pattern:  cwBounded(`[A-CEGHJ-PR-TW-Za-ceghj-pr-tw-z][A-CEGHJ-NPR-TW-Za-ceghj-npr-tw-z] ?\d{2} ?\d{2} ?\d{2} ?[A-Da-d]`),
		keywords: kw("insurance no.", "insurance number", "insurance#", "national insurance number", "nationalinsurance#", "nationalinsurancenumber", "nin", "nino"),
	})
	cwAddIdentifier("NieNumber-ES", cwDataDetector{
		pattern: cwBounded(`[XYZxyz]-?\d{7}-?[A-Za-z]`), keywords: kw(cwSpanishTaxKeywords...), valid: cwValidSpanishID,
	})
	cwAddIdentifier("NifNumber-ES", cwDataDetector{
		pattern: cwBounded(`(?:\d{8}|[KLMXYZklmxyz]-?\d{7})-?[A-Za-z]`), keywords: kw(cwSpanishTaxKeywords...), valid: cwValidSpanishID,
	})
	for country, p := range cwPassportPatterns {
		cwAddIdentifier("PassportNumber-"+country, cwDataDetector{pattern: cwBounded(p.pattern), keywords: kw(p.keywords...)})
	}
	cwAddIdentifier("PermanentResidenceNumber-CA", cwDataDetector{
		pattern:  cwBounded(`[A-Za-z]{2}\d{7}|\d{8}`),
		keywords: kw("carte résident permanent", "numéro carte résident permanent", "numéro résident permanent", "permanent resident card", "permanent resident card number", "permanent resident no", "permanent resident no.", "permanent resident number", "pr no", "pr no.", "pr non", "pr number", "résident permanent no.", "résident permanent non"),
	})
	phoneKeywords := []string{"cell", "contact", "fax", "fax number", "mobile", "phone", "phone number", "tel", "telephone", "telephone number"}
	for country, p := range cwPhonePatterns {
		keywords := phoneKeywords
		if country == "BR" {
			keywords = append([]string{"cel", "celular", "fone", "móvel", "número residencial", "numero residencial", "telefone"}, phoneKeywords...)
		}
		cwAddIdentifier("PhoneNumber-"+country, cwDataDetector{
			pattern:         cwBounded(p.pattern),
			keywords:        kw(keywords...),
			keywordOptional: cwStartsWithCountryCode(p.codes...),
		})
	}
	cwAddIdentifier("PostalCode-CA", cwDataDetector{pattern: cwBounded(`[ABCEGHJ-NPRSTVXY]\d[ABCEGHJ-NPRSTV-Z] ?\d[ABCEGHJ-NPRSTV-Z]\d`)})
	cwAddIdentifier("RgNumber-BR", cwDataDetector{
		pattern: cwBounded(`\d{1,2}\.?\d{3}\.?\d{3}-?[\dXx]`), keywords: kw("registro geral", "rg"),
	})
	cwAddIdentifier("SocialInsuranceNumber-CA", cwDataDetector{
		pattern:  cwBounded(`\d{3}[ -]?\d{3}[ -]?\d{3}`),
		keywords: kw("canadian id", "numéro d'assurance sociale", "social insurance number", "sin"),
		valid:    func(m string) bool { return cwLuhn(cwDigits(m)) },
	})
	cwAddIdentifier("Ssn-ES", cwDataDetector{
		pattern:  cwBounded(`\d{2}[ /-]?\d{8}[ /-]?\d{2}`),
		keywords: kw("número de la seguridad social", "social security no.", "social security no", "social security number", "socialsecurityno#", "ssn", "ssn#"),
	})
	cwAddIdentifier("Ssn-US", cwDataDetector{
		pattern: cwBounded(`\d{3}-\d{2}-\d{4}|\d{9}`), keywords: kw("social security", "ss#", "ssn"), valid: cwValidSSN,
	})
	for country, t := range cwTaxIdentifiers {
		cwAddIdentifier("TaxId-"+country, cwDataDetector{pattern: cwBounded(t.pattern), keywords: kw(t.keywords...)})
	}
	cwAddIdentifier("ZipCode-US", cwDataDetector{pattern: cwBounded(`\d{5}(?:-\d{4})?`), keywords: kw("zip code", "zip+4")})
	cwAddIdentifier("Address", cwDataDetector{
		pattern: regexp.MustCompile(`\b\d{1,6}[A-Za-z]? (?:[A-Z][a-zA-Z.'-]* ){0,4}(?i:street|st|avenue|ave|road|rd|boulevard|blvd|lane|ln|drive|dr|court|ct|way|place|pl|terrace|parkway|pkwy|highway|hwy|strasse|straße|rue|via|calle)\.?,? (?:[A-Z][a-zA-Z.'-]*,? ){1,3}(?:[A-Z]{2,3},? )?(?:\d{5}(?:-\d{4})?|[A-Z]\d[A-Z] ?\d[A-Z]\d|[A-Z]{1,2}\d[A-Z\d]? ?\d[A-Z]{2}|\d{4})\b`),
	})
	cwAddIdentifier("EmailAddress", cwDataDetector{
		pattern: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`),
	})
	cwAddIdentifier("LatLong", cwDataDetector{
		pattern:  cwBounded(`[-+]?(?:90(?:\.0+)?|[1-8]?\d\.\d+), ?[-+]?(?:180(?:\.0+)?|(?:1[0-7]\d|[1-9]?\d)\.\d+)`),
		keywords: kw("coordinate", "coordinates", "lat long", "latitude longitude", "location", "position"),
	})
	cwAddIdentifier("Name", cwDataDetector{
		pattern: regexp.MustCompile(`\b[A-Z][a-z]{1,19}(?: [A-Z]\.)?(?: [A-Z][a-z]{1,19}){1,2}\b`),
		valid:   cwValidName,
	})
	cwAddIdentifier("VehicleIdentificationNumber", cwDataDetector{
		pattern:  cwBounded(`[A-HJ-NPR-Za-hj-npr-z0-9]{17}`),
		keywords: kw("fahrgestellnummer", "niv", "numarul de identificare", "numarul seriei de sasiu", "serie sasiu", "numer vin", "número de identificação do veículo", "número de identificación de automóviles", "numéro d'identification du véhicule", "vehicle identification number", "vin", "vin numeris"),
		valid:    cwHasLetterAndDigit,
	})
}
