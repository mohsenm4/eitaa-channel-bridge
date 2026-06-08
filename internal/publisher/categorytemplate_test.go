package publisher

import (
	"strings"
	"testing"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

const qarzSampleMsg = `📌 گزارش قرض الحسنه - تابستان 1404

📅 1404/06/31

📝

تابستان 1404 – گزارش ماهانه واحد قرض الحسنه از تاریخ 1404/04/01 تا 1404/06/31

تعداد وام های تایید شده: 15
مبلغ کل وام های اعطایی: 2,800,000,000 ریال

موضوعات و تعداد وام های اعطایی:
- رهن منزل: 4
- معوقات: 5
- درمان: 3
- تحصیل: 3

مجموع مبلغ وام های اعطایی به رقم 2,800,000,000 ریال می رسد. این رقم در قالب 15 وام به خانواده های مضطر و نیازمند پرداخت شد.

#قرض_الحسنه`

func qarzRouted() router.Routed {
	return router.Routed{
		Message:    eitaa.Message{Text: qarzSampleMsg},
		Category:   "qarz-al-hasaneh",
		CategoryFa: "واحد قرض الحسنه",
		Title:      "گزارش قرض الحسنه - تابستان 1404",
		EventDate:  "1404/06/31",
		Hashtags:   []string{"قرض_الحسنه"},
	}
}

func TestParseQarzReport_ExtractsAllFields(t *testing.T) {
	vars := parseQarzReport(qarzRouted(), 99999)
	checks := map[string]string{
		"FEATURED_ID":  "99999",
		"EVENT_DATE":   "1404/06/31",
		"LOAN_COUNT":   "15",
		"TOTAL_AMOUNT": "2,800,000,000",
	}
	for k, want := range checks {
		if got := vars[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(vars["PERIOD_DESCRIPTION"], "تابستان 1404") ||
		!strings.Contains(vars["PERIOD_DESCRIPTION"], "1404/04/01") {
		t.Errorf("PERIOD_DESCRIPTION = %q, missing تابستان/date range", vars["PERIOD_DESCRIPTION"])
	}
	if !strings.Contains(vars["SUMMARY"], "مجموع مبلغ") ||
		!strings.Contains(vars["SUMMARY"], "2,800,000,000") {
		t.Errorf("SUMMARY = %q, missing مجموع/total", vars["SUMMARY"])
	}
	for _, want := range []string{
		"رهن منزل: <strong>4</strong>",
		"معوقات: <strong>5</strong>",
		"درمان: <strong>3</strong>",
		"تحصیل: <strong>3</strong>",
	} {
		if !strings.Contains(vars["BREAKDOWN_LIST"], want) {
			t.Errorf("BREAKDOWN_LIST missing %q\ngot:\n%s", want, vars["BREAKDOWN_LIST"])
		}
	}
}

func TestRenderCategoryTemplate_QarzEndToEnd(t *testing.T) {
	tmpl, ok := loadCategoryTemplate("qarz-al-hasaneh")
	if !ok {
		t.Fatal("qarz-al-hasaneh.tmpl not embedded")
	}
	vars := parseQarzReport(qarzRouted(), 42)
	out := renderCategoryTemplate(tmpl, vars)

	for _, want := range []string{
		`image="42"`,
		"قال امیر المومنین علی علیه السلام",
		"تعداد وام‌های تایید شده: <strong>15</strong>",
		"مبلغ کل وام‌های اعطایی: <strong>2,800,000,000</strong> ریال",
		"رهن منزل: <strong>4</strong>",
		"معوقات: <strong>5</strong>",
		"📅 1404/06/31",
		"مجموع مبلغ وام های اعطایی",
		"[vc_row][vc_column]",
		"[/vc_column][/vc_row]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- full output ---\n%s", want, out)
		}
	}
	// No leftover placeholders.
	if strings.Contains(out, "{{") {
		t.Errorf("unfilled placeholder remains:\n%s", out)
	}
}

func TestExtractCategoryVars_UnknownSlugReturnsNil(t *testing.T) {
	msg := qarzRouted()
	msg.Category = "hemayat-khedmat"
	if vars := extractCategoryVars(msg, 0); vars != nil {
		t.Errorf("expected nil for category without parser, got %v", vars)
	}
}

const tavanmandSampleMsg = `📌 کارگاه آموزشی تربیت مربی – آبان ۱۴۰۴

📅 1404/08/15

📝

کارگاه آموزشی تربیت مربی واحد توانمندسازی موسسه فاطمیون در تاریخ ۱۵ آبان ۱۴۰۴ با حضور ۲۸ نفر از همکاران برگزار شد.

موضوعات اصلی کارگاه: روش‌های نوین آموزش، ارتباط موثر با مخاطب، طراحی محتوای جذاب.

هزینه اجرای طرح: 12,000,000 ریال

#توانمندسازی`

func tavanmandRouted() router.Routed {
	return router.Routed{
		Message:    eitaa.Message{Text: tavanmandSampleMsg},
		Category:   "tavanmandsazi",
		CategoryFa: "واحد توانمندسازی",
		Title:      "کارگاه آموزشی تربیت مربی – آبان ۱۴۰۴",
		EventDate:  "1404/08/15",
		Hashtags:   []string{"توانمندسازی"},
	}
}

func TestParseSimpleReport_KeepsBodyDropsMarkersAndHashtags(t *testing.T) {
	vars := parseSimpleReport(tavanmandRouted(), 555)
	if vars["TITLE"] != "کارگاه آموزشی تربیت مربی – آبان ۱۴۰۴" {
		t.Errorf("TITLE = %q", vars["TITLE"])
	}
	if vars["EVENT_DATE"] != "1404/08/15" {
		t.Errorf("EVENT_DATE = %q", vars["EVENT_DATE"])
	}
	body := vars["BODY"]
	for _, want := range []string{
		"کارگاه آموزشی تربیت مربی واحد توانمندسازی",
		"موضوعات اصلی کارگاه",
		"12,000,000",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("BODY missing %q\n--- body ---\n%s", want, body)
		}
	}
	for _, leak := range []string{"📌", "📅", "📝", "#توانمندسازی"} {
		if strings.Contains(body, leak) {
			t.Errorf("BODY should not contain %q\n--- body ---\n%s", leak, body)
		}
	}
}

func TestRenderCategoryTemplate_TavanmandsaziEndToEnd(t *testing.T) {
	tmpl, ok := loadCategoryTemplate("tavanmandsazi")
	if !ok {
		t.Fatal("tavanmandsazi.tmpl not embedded")
	}
	vars := parseSimpleReport(tavanmandRouted(), 555)
	out := renderCategoryTemplate(tmpl, vars)

	for _, want := range []string{
		"کارگاه آموزشی تربیت مربی",
		"📅 تاریخ برگزاری: 1404/08/15",
		`<h3 style="text-align: center;">کارگاه آموزشی تربیت مربی – آبان ۱۴۰۴</h3>`,
		"[vc_row][vc_column][vc_column_text]",
		"[/vc_column_text][/vc_column][/vc_row]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- full output ---\n%s", want, out)
		}
	}
	// Featured image must NOT be injected — the theme renders it from
	// featured_media, not from a [vc_single_image] shortcode.
	if strings.Contains(out, "vc_single_image") {
		t.Errorf("vc_single_image should not appear in narrative template (theme handles featured_media)\n%s", out)
	}
	if strings.Contains(out, "{{") {
		t.Errorf("unfilled placeholder remains:\n%s", out)
	}
}

func TestExtractCategoryVars_TavanmandsaziUsesSimpleParser(t *testing.T) {
	vars := extractCategoryVars(tavanmandRouted(), 555)
	if vars == nil {
		t.Fatal("expected non-nil vars for tavanmandsazi")
	}
	if _, ok := vars["BODY"]; !ok {
		t.Error("tavanmandsazi should have BODY (uses parseSimpleReport)")
	}
	if _, ok := vars["LOAN_COUNT"]; ok {
		t.Error("tavanmandsazi should NOT have LOAN_COUNT (that's qarz-specific)")
	}
}

func TestRezvan_RoutesToSimpleTemplateAndParser(t *testing.T) {
	msg := tavanmandRouted()
	msg.Category = "rezvan"
	msg.CategoryFa = "رضوان"

	if _, ok := loadCategoryTemplate("rezvan"); !ok {
		t.Fatal("rezvan.tmpl not embedded")
	}
	vars := extractCategoryVars(msg, 777)
	if vars == nil {
		t.Fatal("expected non-nil vars for rezvan")
	}
	if vars["TITLE"] == "" || vars["BODY"] == "" || vars["EVENT_DATE"] == "" {
		t.Errorf("simple parser fields missing: %#v", vars)
	}

	tmpl, _ := loadCategoryTemplate("rezvan")
	out := renderCategoryTemplate(tmpl, vars)
	if strings.Contains(out, "{{") {
		t.Errorf("unfilled placeholder remains:\n%s", out)
	}
	if !strings.Contains(out, "📅 تاریخ برگزاری: 1404/08/15") {
		t.Errorf("date line missing in rezvan output:\n%s", out)
	}
}
