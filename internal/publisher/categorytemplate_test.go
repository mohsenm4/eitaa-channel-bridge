package publisher

import (
	"strings"
	"testing"

	"github.com/mohsenm4/eitaa-channel-bridge/internal/eitaa"
	"github.com/mohsenm4/eitaa-channel-bridge/internal/router"
)

func TestExtractCategoryVars_UnknownSlugReturnsNil(t *testing.T) {
	msg := tavanmandRouted()
	msg.Category = "no-such-category"
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

func TestParseSimpleReport_DropsEverythingAfterHashtagLine(t *testing.T) {
	msg := tavanmandRouted()
	msg.Message.Text = tavanmandSampleMsg + "\n\n🔰 کانال رسمی موسسه خدمات اجتماعی فاطمیون\n🆔 https://eitaa.com/fatemyoon_ir\nهر خط دیگه‌ای بعد از هشتگ"
	body := parseSimpleReport(msg, 0)["BODY"]
	for _, banned := range []string{"کانال رسمی", "eitaa.com", "🆔", "🔰", "هر خط دیگه"} {
		if strings.Contains(body, banned) {
			t.Errorf("BODY should not contain %q\n--- body ---\n%s", banned, body)
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
}

func TestNojavanan_RoutesToSimpleTemplateAndParser(t *testing.T) {
	msg := tavanmandRouted()
	msg.Category = "nojavanan"
	msg.CategoryFa = "واحد نوجوانان"

	if _, ok := loadCategoryTemplate("nojavanan"); !ok {
		t.Fatal("nojavanan.tmpl not embedded")
	}
	vars := extractCategoryVars(msg, 777)
	if vars == nil {
		t.Fatal("expected non-nil vars for nojavanan")
	}
	if vars["TITLE"] == "" || vars["BODY"] == "" || vars["EVENT_DATE"] == "" {
		t.Errorf("simple parser fields missing: %#v", vars)
	}

	tmpl, _ := loadCategoryTemplate("nojavanan")
	out := renderCategoryTemplate(tmpl, vars)
	if strings.Contains(out, "{{") {
		t.Errorf("unfilled placeholder remains:\n%s", out)
	}
	if !strings.Contains(out, "📅 تاریخ برگزاری: 1404/08/15") {
		t.Errorf("date line missing in nojavanan output:\n%s", out)
	}
}

func TestHemayatKhedmat_RoutesToSimpleTemplateAndParser(t *testing.T) {
	msg := tavanmandRouted()
	msg.Category = "hemayat-khedmat"
	msg.CategoryFa = "حمایت و خدمت"

	if _, ok := loadCategoryTemplate("hemayat-khedmat"); !ok {
		t.Fatal("hemayat-khedmat.tmpl not embedded")
	}
	vars := extractCategoryVars(msg, 111)
	if vars == nil {
		t.Fatal("expected non-nil vars for hemayat-khedmat")
	}
	tmpl, _ := loadCategoryTemplate("hemayat-khedmat")
	out := renderCategoryTemplate(tmpl, vars)
	if strings.Contains(out, "{{") {
		t.Errorf("unfilled placeholder remains:\n%s", out)
	}
	if !strings.Contains(out, "📅 تاریخ برگزاری: 1404/08/15") {
		t.Errorf("date line missing:\n%s", out)
	}
}

func TestAkhbarEttelaiyeh_RoutesToSimpleTemplateAndParser(t *testing.T) {
	// News & announcements posts use the same narrative parser as the four
	// existing report categories — title + date + body, no special handling.
	msg := tavanmandRouted()
	msg.Category = "akhbar-etelaiyeh"
	msg.CategoryFa = "اخبار و اطلاعیه‌ها"

	if _, ok := loadCategoryTemplate("akhbar-etelaiyeh"); !ok {
		t.Fatal("akhbar-etelaiyeh.tmpl not embedded")
	}
	vars := extractCategoryVars(msg, 222)
	if vars == nil {
		t.Fatal("expected non-nil vars for akhbar-etelaiyeh")
	}
	if vars["TITLE"] == "" || vars["BODY"] == "" || vars["EVENT_DATE"] == "" {
		t.Errorf("simple parser fields missing: %#v", vars)
	}

	tmpl, _ := loadCategoryTemplate("akhbar-etelaiyeh")
	out := renderCategoryTemplate(tmpl, vars)
	if strings.Contains(out, "{{") {
		t.Errorf("unfilled placeholder remains:\n%s", out)
	}
	if !strings.Contains(out, "📅 تاریخ: 1404/08/15") {
		t.Errorf("date line missing in akhbar-etelaiyeh output:\n%s", out)
	}
	if !strings.Contains(out, `<h3 style="text-align: center;">کارگاه آموزشی تربیت مربی – آبان ۱۴۰۴</h3>`) {
		t.Errorf("title heading missing:\n%s", out)
	}
	if strings.Contains(out, "vc_single_image") {
		t.Errorf("vc_single_image should not appear in akhbar-etelaiyeh template:\n%s", out)
	}
}

func TestQarzAlHasaneh_RoutesToSimpleTemplateAndParser(t *testing.T) {
	msg := tavanmandRouted()
	msg.Category = "qarz-al-hasaneh"
	msg.CategoryFa = "واحد قرض الحسنه"

	if _, ok := loadCategoryTemplate("qarz-al-hasaneh"); !ok {
		t.Fatal("qarz-al-hasaneh.tmpl not embedded")
	}
	vars := extractCategoryVars(msg, 888)
	if vars == nil {
		t.Fatal("expected non-nil vars for qarz-al-hasaneh")
	}
	if vars["TITLE"] == "" || vars["BODY"] == "" || vars["EVENT_DATE"] == "" {
		t.Errorf("simple parser fields missing: %#v", vars)
	}

	tmpl, _ := loadCategoryTemplate("qarz-al-hasaneh")
	out := renderCategoryTemplate(tmpl, vars)
	if strings.Contains(out, "{{") {
		t.Errorf("unfilled placeholder remains:\n%s", out)
	}
	if !strings.Contains(out, "📅 تاریخ برگزاری: 1404/08/15") {
		t.Errorf("date line missing in qarz output:\n%s", out)
	}
	if strings.Contains(out, "vc_single_image") {
		t.Errorf("vc_single_image should not appear in simple qarz template:\n%s", out)
	}
}
