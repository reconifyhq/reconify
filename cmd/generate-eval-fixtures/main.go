// generate-eval-fixtures (re)writes the input files of the generated scenarios
// in the agent evaluation corpus. Every byte is a pure function of the code in
// this package: layouts are fixed and any randomness comes from math/rand
// sources with explicit seeds, so the committed fixtures are reproducible and
// a unit test can prove they have not drifted.
//
// Usage:
//
//	go run ./cmd/generate-eval-fixtures -corpus evals
//
// Only inputs are generated. Answer keys (expected/result.json and
// expected/explanation.json) come from running the reference config through
// the real CLI, so they stay an independent check on these fixtures.
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// scaleSeed fixes the pseudo-random stream behind scenario 012.
const scaleSeed = 20240112

func main() {
	corpus := flag.String("corpus", "evals", "evaluation corpus directory")
	flag.Parse()
	if err := writeFixtures(*corpus); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// writeFixtures writes every generated file below corpus.
func writeFixtures(corpus string) error {
	files, err := fixtures()
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		dst := filepath.Join(corpus, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(dst, files[rel], 0o600); err != nil { // #nosec G703 -- checked-in corpus path.
			return err
		}
	}
	return nil
}

// fixtures returns every generated file keyed by its path relative to the
// corpus root.
func fixtures() (map[string][]byte, error) {
	files := map[string][]byte{}
	builders := []func() (map[string][]byte, error){
		europeanExport,
		accountingNegatives,
		mixedFormats,
		scaleExceptions,
		renamedColumn,
		stalePattern,
		repeatedReferences,
	}
	for _, build := range builders {
		built, err := build()
		if err != nil {
			return nil, err
		}
		for path, data := range built {
			if _, dup := files[path]; dup {
				return nil, fmt.Errorf("fixture %s generated twice", path)
			}
			files[path] = data
		}
	}
	return files, nil
}

var standardHeader = []string{"date", "amount", "currency", "reference", "description"}

func csvBytes(rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	if err := csv.NewWriter(&buf).WriteAll(rows); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func csvFiles(files map[string][][]string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(files))
	for path, rows := range files {
		data, err := csvBytes(rows)
		if err != nil {
			return nil, err
		}
		out[path] = data
	}
	return out, nil
}

// 009: the bank portal is European. Dates are day-first with dots, amounts use
// a comma decimal mark and a dot thousands separator (so they are quoted), and
// the column names differ from the ledger's. Every export day past the 12th
// makes a day/month mix-up fail loudly; the amounts make a US-style reading
// fail silently, which is the trap.
func europeanExport() (map[string][]byte, error) {
	return csvFiles(map[string][][]string{
		"009-european-export/inputs/left.csv": {
			standardHeader,
			{"2024-01-03", "1250.00", "EUR", "INV-9001", "Consulting retainer"},
			{"2024-01-04", "89.90", "EUR", "INV-9002", "Office supplies"},
			{"2024-01-05", "12400.50", "EUR", "INV-9003", "Equipment lease"},
			{"2024-01-08", "310.00", "EUR", "INV-9004", "Software subscription"},
			{"2024-01-09", "560.00", "EUR", "INV-9005", "Training workshop"},
			{"2024-01-10", "75.25", "EUR", "INV-9006", "Courier fees"},
			{"2024-01-15", "200.00", "EUR", "INV-9007", "Maintenance contract"},
			{"2024-01-25", "3499.99", "EUR", "INV-9008", "Hardware order"},
		},
		"009-european-export/inputs/right.csv": {
			{"Booking date", "Amount", "Currency", "Reference", "Details"},
			{"03.01.2024", "1.250,00", "EUR", "INV-9001", "SEPA credit transfer"},
			{"05.01.2024", "89,90", "EUR", "INV-9002", "SEPA credit transfer"},
			{"05.01.2024", "12.400,50", "EUR", "INV-9003", "SEPA credit transfer"},
			{"09.01.2024", "310,00", "EUR", "INV-9004", "SEPA credit transfer"},
			{"09.01.2024", "506,00", "EUR", "INV-9005", "SEPA credit transfer"},
			{"11.01.2024", "1.999,99", "EUR", "REF-7731", "Unidentified credit"},
			{"21.01.2024", "200,00", "EUR", "INV-9007", "SEPA credit transfer"},
			{"26.01.2024", "3.499,99", "EUR", "INV-9008", "SEPA credit transfer"},
		},
	})
}

// 010: both exports come from a report writer that groups thousands with a
// comma (so large amounts are quoted) and shows money going out, such as
// credit-note refunds, in brackets instead of with a minus sign.
func accountingNegatives() (map[string][]byte, error) {
	return csvFiles(map[string][][]string{
		"010-accounting-negatives/inputs/left.csv": {
			standardHeader,
			{"2024-02-01", "1,250.00", "USD", "INV-1001", "Annual license"},
			{"2024-02-02", "(320.00)", "USD", "CN-1002", "Credit note refund"},
			{"2024-02-05", "(1,075.50)", "USD", "CN-1003", "Credit note refund"},
			{"2024-02-06", "15,000.00", "USD", "INV-1004", "Project milestone"},
			{"2024-02-07", "(45.00)", "USD", "CN-1005", "Credit note refund"},
			{"2024-02-08", "2,310.75", "USD", "INV-1006", "Support plan"},
		},
		"010-accounting-negatives/inputs/right.csv": {
			standardHeader,
			{"2024-02-01", "1,250.00", "USD", "INV-1001", "Wire in"},
			{"2024-02-02", "(320.00)", "USD", "CN-1002", "Wire out"},
			{"2024-02-05", "(1,075.50)", "USD", "CN-1003", "Wire out"},
			{"2024-02-06", "14,990.00", "USD", "INV-1004", "Wire in"},
			{"2024-02-08", "2,310.75", "USD", "INV-1006", "Wire in"},
			{"2024-02-09", "(18.00)", "USD", "BANK-CHG", "Account fee"},
		},
	})
}

// bankJSONRow is one object of the bank's JSON export.
type bankJSONRow struct {
	BookedAt   string      `json:"booked_at"`
	Value      json.Number `json:"value"`
	Ccy        string      `json:"ccy"`
	EndToEndID string      `json:"end_to_end_id"`
	Narrative  string      `json:"narrative"`
}

// 011: the ledger is a CSV with its own column names and 03-Jan-2024 dates,
// while the bank can only export JSON, with full timestamps and different
// field names. The bank narrative is free text that must not be mistaken for
// the invoice reference.
func mixedFormats() (map[string][]byte, error) {
	ledger, err := csvBytes([][]string{
		{"Posting Date", "Amount", "Currency", "Invoice", "Memo"},
		{"03-Jan-2024", "1250.00", "USD", "INV-2001", "Annual support"},
		{"04-Jan-2024", "89.90", "USD", "INV-2002", "Hardware accessories"},
		{"08-Jan-2024", "430.00", "USD", "INV-2003", "Implementation fee"},
		{"09-Jan-2024", "2150.00", "USD", "INV-2004", "Platform subscription"},
		{"10-Jan-2024", "75.25", "USD", "INV-2005", "Courier fees"},
		{"15-Jan-2024", "980.00", "USD", "INV-2006", "Quarterly retainer"},
	})
	if err != nil {
		return nil, err
	}
	bank, err := json.MarshalIndent([]bankJSONRow{
		{"2024-01-03T14:22:05Z", "1250.00", "USD", "INV-2001", "ACME LTD PAYMENT"},
		{"2024-01-05T09:03:41Z", "89.90", "USD", "INV-2002", "WIDGETS INC INV 2002"},
		{"2024-01-08T16:45:10Z", "430.00", "USD", "INV-2003", "GLOBEX PAYMENT"},
		{"2024-01-09T11:12:59Z", "2105.00", "USD", "INV-2004", "INITECH PAYMENT"},
		{"2024-01-12T08:30:00Z", "512.40", "USD", "BANK-7731", "UNREFERENCED CREDIT"},
		{"2024-01-16T13:05:27Z", "980.00", "USD", "INV-2006", "HOOLI PAYMENT"},
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"011-mixed-formats/inputs/left.csv":   ledger,
		"011-mixed-formats/inputs/right.json": append(bank, '\n'),
	}, nil
}

// scaleTx is one ledger transaction of scenario 012.
type scaleTx struct {
	date  time.Time
	cents int64
	ref   string
	desc  string
	lag   int // days the bank takes to post it
}

// Planted exceptions of scenario 012, as indices into the ledger.
var (
	scaleMissingFromBank = map[int]bool{17: true, 88: true}
	scaleAmountDiff      = map[int]int64{41: -2500, 103: -4075} // bank minus ledger, in cents
	scaleTimingDiff      = map[int]int{29: 6, 74: 9}            // lag in days beyond the 2-day window
)

const scaleRows = 120

// 012: a month of clean card sales, 120 rows per side, so format inference has
// enough rows to be confident, with a handful of planted exceptions.
func scaleExceptions() (map[string][]byte, error) {
	rng := rand.New(rand.NewSource(scaleSeed)) // #nosec G404 -- deterministic fixture data, not security-sensitive.
	merchants := []string{
		"Northwind Traders", "Contoso Retail", "Fabrikam Goods", "Tailspin Toys", "Litware Foods",
		"Adventure Works", "Wingtip Gifts", "Proseware Cafe", "Coho Vineyard", "Lucerne Publishing",
	}
	lags := []int{0, 0, 1, 2}
	start := time.Date(2024, time.January, 2, 0, 0, 0, 0, time.UTC)

	ledger := make([]scaleTx, scaleRows)
	for i := range ledger {
		ledger[i] = scaleTx{
			date:  start.AddDate(0, 0, rng.Intn(24)),
			cents: int64(1000 + rng.Intn(240000)),
			ref:   fmt.Sprintf("TX-2401-%04d", i+1),
			desc:  merchants[rng.Intn(len(merchants))] + " sale",
			lag:   lags[rng.Intn(len(lags))],
		}
	}
	sort.SliceStable(ledger, func(i, j int) bool { return ledger[i].date.Before(ledger[j].date) })
	// References follow ledger order, so the planted indices above are stable.
	for i := range ledger {
		ledger[i].ref = fmt.Sprintf("TX-2401-%04d", i+1)
	}

	left := [][]string{standardHeader}
	var bankRows []scaleTx
	for i, tx := range ledger {
		left = append(left, []string{tx.date.Format("2006-01-02"), money(tx.cents), "USD", tx.ref, tx.desc})
		if scaleMissingFromBank[i] {
			continue
		}
		posted := tx
		posted.desc = tx.desc + " settlement"
		if diff, ok := scaleAmountDiff[i]; ok {
			posted.cents += diff
		}
		if lag, ok := scaleTimingDiff[i]; ok {
			posted.lag = lag
		}
		posted.date = tx.date.AddDate(0, 0, posted.lag)
		bankRows = append(bankRows, posted)
	}
	bankRows = append(bankRows, scaleTx{
		date: time.Date(2024, time.January, 19, 0, 0, 0, 0, time.UTC), cents: 18750,
		ref: "TX-2401-0999", desc: "Unidentified card settlement",
	})
	sort.SliceStable(bankRows, func(i, j int) bool {
		if !bankRows[i].date.Equal(bankRows[j].date) {
			return bankRows[i].date.Before(bankRows[j].date)
		}
		return bankRows[i].ref < bankRows[j].ref
	})
	right := [][]string{standardHeader}
	for _, tx := range bankRows {
		right = append(right, []string{tx.date.Format("2006-01-02"), money(tx.cents), "USD", tx.ref, tx.desc})
	}
	return csvFiles(map[string][][]string{
		"012-scale-exceptions/inputs/left.csv":  left,
		"012-scale-exceptions/inputs/right.csv": right,
	})
}

func money(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }

// 013: the bank renamed its payment identifier column. The workspace starts
// with a working config that still names the old column; its matching rules
// (three-day settlement lag, a fee of up to 1.50) are not derivable from the
// files and must survive the repair.
func renamedColumn() (map[string][]byte, error) {
	return csvFiles(map[string][][]string{
		"013-repair-renamed-column/inputs/ledger.csv": {
			standardHeader,
			{"2024-03-01", "500.00", "GBP", "PAY-3001", "Card sale"},
			{"2024-03-04", "200.00", "GBP", "PAY-3002", "Card sale"},
			{"2024-03-05", "100.00", "GBP", "PAY-3003", "Card sale"},
			{"2024-03-06", "750.00", "GBP", "PAY-3004", "Card sale"},
			{"2024-03-07", "320.00", "GBP", "PAY-3005", "Card sale"},
			{"2024-03-11", "60.00", "GBP", "PAY-3006", "Card sale"},
		},
		"013-repair-renamed-column/inputs/bank.csv": {
			{"date", "amount", "currency", "end_to_end_id", "description"},
			{"2024-03-01", "500.00", "GBP", "PAY-3001", "Card settlement"},
			{"2024-03-07", "200.00", "GBP", "PAY-3002", "Card settlement"},
			{"2024-03-05", "98.50", "GBP", "PAY-3003", "Card settlement less fee"},
			{"2024-03-06", "740.00", "GBP", "PAY-3004", "Card settlement less fee"},
			{"2024-03-14", "320.00", "GBP", "PAY-3005", "Card settlement"},
			{"2024-03-12", "35.00", "GBP", "PAY-9999", "Unidentified credit"},
		},
	})
}

// 014: the bank renamed its monthly export files, so the glob in the existing
// config matches nothing. The folder also still holds last month's statement,
// which sorts first under a broader glob and which the engine would silently
// pick instead of the January export.
func stalePattern() (map[string][]byte, error) {
	return csvFiles(map[string][][]string{
		"014-repair-stale-pattern/inputs/ledger.csv": {
			standardHeader,
			{"2024-01-10", "120.00", "USD", "REF-4001", "Supplier payment"},
			{"2024-01-15", "80.00", "USD", "REF-4002", "Supplier payment"},
			{"2024-01-22", "45.00", "USD", "REF-4003", "Supplier payment"},
			{"2024-01-24", "300.00", "USD", "REF-4004", "Supplier payment"},
			{"2024-01-29", "99.00", "USD", "REF-4006", "Supplier payment"},
		},
		"014-repair-stale-pattern/inputs/bank_export_jan.csv": {
			standardHeader,
			{"2024-01-11", "120.00", "USD", "REF-4001", "Outgoing payment"},
			{"2024-01-15", "80.00", "USD", "REF-4002", "Outgoing payment"},
			{"2024-01-24", "300.00", "USD", "REF-4004", "Outgoing payment"},
			{"2024-01-29", "90.00", "USD", "REF-4006", "Outgoing payment"},
			{"2024-01-30", "12.00", "USD", "REF-4099", "Unidentified debit"},
		},
		"014-repair-stale-pattern/inputs/bank_2023-12.csv": {
			standardHeader,
			{"2023-12-18", "250.00", "USD", "REF-3901", "Outgoing payment"},
			{"2023-12-28", "75.00", "USD", "REF-3902", "Outgoing payment"},
		},
	})
}

// 015: the bank shows one invoice number on two rows whose amounts sum to the
// ledger amount. That is either a customer paying in two parts or a wrongly
// re-used reference, and finance has not decided which. The fixture holds
// exactly one repeated invoice and one leftover bank row: the engine does not
// order several duplicate groups or unmatched rows deterministically, and an
// answer key must be byte-stable.
func repeatedReferences() (map[string][]byte, error) {
	return csvFiles(map[string][][]string{
		"015-ask-user-repeated-references/inputs/left.csv": {
			standardHeader,
			{"2024-04-02", "100.00", "USD", "INV-5001", "Invoice"},
			{"2024-04-03", "30.00", "USD", "INV-5002", "Invoice"},
			{"2024-04-04", "220.00", "USD", "INV-5003", "Invoice"},
		},
		"015-ask-user-repeated-references/inputs/right.csv": {
			standardHeader,
			{"2024-04-02", "60.00", "USD", "INV-5001", "Customer payment"},
			{"2024-04-03", "40.00", "USD", "INV-5001", "Customer payment"},
			{"2024-04-03", "30.00", "USD", "INV-5002", "Customer payment"},
			{"2024-04-04", "220.00", "USD", "INV-5003", "Customer payment"},
		},
	})
}
