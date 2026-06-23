<?php
// Endpoints for the homepage "اخبار و اطلاعیه ها" three-card news section:
//   /news-section-source     — diagnostic: extract the section's raw shortcode source
//   /rotate-news-section     — push a single new post on top and drop the third (used on publish)
//   /reconcile-news-section  — rebuild the cards from the top-3 newest posts in a category (used on delete & startup)

if (!defined('ABSPATH')) {
    exit;
}

add_action('rest_api_init', function () {
    register_rest_route('eitaa-bridge/v1', '/news-section-source', [
        'methods'             => 'GET',
        'permission_callback' => function () {
            return current_user_can('edit_pages');
        },
        'callback' => 'eitaa_bridge_news_section_source',
        'args'     => [
            'page_id'       => ['required' => true, 'type' => 'integer'],
            'heading_text'  => ['type' => 'string', 'default' => 'اخبار و اطلاعیه ها'],
        ],
    ]);

    register_rest_route('eitaa-bridge/v1', '/rotate-news-section', [
        'methods'             => 'POST',
        'permission_callback' => function () {
            return current_user_can('edit_pages');
        },
        'callback' => 'eitaa_bridge_rotate_news_section',
        'args'     => [
            'page_id'      => ['required' => true, 'type' => 'integer'],
            'post_id'      => ['required' => true, 'type' => 'integer'],
            'heading_text' => ['type' => 'string', 'default' => 'اخبار و اطلاعیه ها'],
        ],
    ]);

    register_rest_route('eitaa-bridge/v1', '/reconcile-news-section', [
        'methods'             => 'POST',
        'permission_callback' => function () {
            return current_user_can('edit_pages');
        },
        'callback' => 'eitaa_bridge_reconcile_news_section',
        'args'     => [
            'page_id'       => ['required' => true, 'type' => 'integer'],
            'category_slug' => ['required' => true, 'type' => 'string'],
            'heading_text'  => ['type' => 'string', 'default' => 'اخبار و اطلاعیه ها'],
        ],
    ]);
});

// --- diagnostic ----------------------------------------------------------

function eitaa_bridge_news_section_source(WP_REST_Request $req) {
    $page_id      = (int) $req->get_param('page_id');
    $heading_text = trim((string) $req->get_param('heading_text'));
    $page         = get_post($page_id);
    if (!$page) {
        return new WP_Error('eitaa_bridge_not_found',
            sprintf('page %d not found', $page_id),
            ['status' => 404]);
    }
    if ($heading_text === '') {
        return new WP_Error('eitaa_bridge_bad_heading',
            'heading_text is required and must not be empty',
            ['status' => 400]);
    }

    $out = [
        'page_id'      => $page_id,
        'heading_text' => $heading_text,
        'sources'      => [],
    ];

    foreach (['_aviaLayoutBuilderCleanData' => 'clean_data', 'post_content' => 'post_content'] as $field => $label) {
        $haystack = $field === 'post_content'
            ? (string) $page->post_content
            : (string) get_post_meta($page_id, $field, true);
        if ($haystack === '') {
            $out['sources'][$label] = ['present' => false];
            continue;
        }
        $out['sources'][$label] = eitaa_bridge_news_extract_section($haystack, $heading_text);
        $out['sources'][$label]['present'] = true;
    }
    return $out;
}

function eitaa_bridge_news_extract_section(string $haystack, string $heading_text): array {
    $heading_pos = mb_strpos($haystack, $heading_text);
    if ($heading_pos === false) {
        return [
            'heading_found' => false,
            'note'          => 'heading_text not found anywhere in this source',
        ];
    }
    $byte_pos = strlen(mb_substr($haystack, 0, $heading_pos));

    $rest = substr($haystack, $byte_pos);
    $end_offset = strlen($rest);
    if (preg_match('#\[av_(heading|special_heading)\b#u', $rest, $m, PREG_OFFSET_CAPTURE, mb_strlen($heading_text))) {
        $end_offset = $m[0][1];
    }
    $section = substr($rest, 0, $end_offset);

    return [
        'heading_found'      => true,
        'heading_byte_pos'   => $byte_pos,
        'section_byte_len'   => strlen($section),
        'section'            => $section,
    ];
}

// --- rotate (single-post, used on publish) -------------------------------

function eitaa_bridge_rotate_news_section(WP_REST_Request $req) {
    $page_id      = (int) $req->get_param('page_id');
    $post_id      = (int) $req->get_param('post_id');
    $heading_text = trim((string) $req->get_param('heading_text'));

    $page = get_post($page_id);
    if (!$page) {
        return new WP_Error('eitaa_bridge_not_found',
            sprintf('page %d not found', $page_id),
            ['status' => 404]);
    }
    $post = get_post($post_id);
    if (!$post) {
        return new WP_Error('eitaa_bridge_not_found',
            sprintf('post %d not found', $post_id),
            ['status' => 404]);
    }
    if ($heading_text === '') {
        return new WP_Error('eitaa_bridge_bad_heading',
            'heading_text must be non-empty',
            ['status' => 400]);
    }

    $post_title    = get_the_title($post_id);
    $post_link     = get_permalink($post_id);
    $thumb_id      = (int) get_post_thumbnail_id($post_id);
    $thumb_url     = $thumb_id > 0 ? (string) wp_get_attachment_url($thumb_id) : '';
    if ($post_title === '' || $post_link === '' || $thumb_id <= 0 || $thumb_url === '') {
        return new WP_Error('eitaa_bridge_post_incomplete',
            sprintf('post %d is missing title, permalink, or featured image — cannot build news card', $post_id),
            ['status' => 400]);
    }

    $touched         = [];
    $skipped_already = false;

    foreach (['_aviaLayoutBuilderCleanData', 'post_content'] as $field) {
        $haystack = $field === 'post_content'
            ? (string) $page->post_content
            : (string) get_post_meta($page_id, $field, true);
        if ($haystack === '') {
            continue;
        }
        $result = eitaa_bridge_rotate_news_in_text($haystack, $heading_text,
            $post_link, $thumb_url, $thumb_id, $post_title);
        if (is_wp_error($result)) {
            return $result;
        }
        if ($result['already_top']) {
            $skipped_already = true;
            continue;
        }
        if ($result['rewrote']) {
            if ($field === 'post_content') {
                wp_update_post(['ID' => $page_id, 'post_content' => $result['text']], true);
            } else {
                update_post_meta($page_id, $field, $result['text']);
            }
            $touched[] = $field;
        }
    }

    if ($skipped_already && count($touched) === 0) {
        return [
            'page_id'  => $page_id,
            'post_id'  => $post_id,
            'rotated'  => false,
            'reason'   => 'first card already points at this post — nothing to do',
            'touched'  => [],
        ];
    }

    if (count($touched) === 0) {
        return new WP_Error('eitaa_bridge_section_not_found',
            sprintf('no news section with heading %q and three cards found on page %d', $heading_text, $page_id),
            ['status' => 404]);
    }

    $caches = eitaa_bridge_bust_caches($page_id);

    return [
        'page_id'        => $page_id,
        'post_id'        => $post_id,
        'rotated'        => true,
        'touched'        => $touched,
        'cleared_caches' => $caches['cleared_caches'],
        'cache_actions'  => $caches['cache_actions'],
        'new_title'      => $post_title,
        'new_link'       => $post_link,
        'new_image_id'   => $thumb_id,
    ];
}

// Pure-string rotation, runs against CleanData and post_content alike.
function eitaa_bridge_rotate_news_in_text(string $haystack, string $heading_text,
                                          string $new_link, string $new_image_url, int $new_image_id,
                                          string $new_title) {
    $needle = "heading='" . $heading_text . "'";
    $h_pos  = strpos($haystack, $needle);
    if ($h_pos === false) {
        return [
            'text'         => $haystack,
            'rewrote'      => false,
            'already_top'  => false,
        ];
    }

    $cards = [];
    $cursor = $h_pos;
    for ($i = 0; $i < 3; $i++) {
        $start = strpos($haystack, '[av_one_third', $cursor);
        if ($start === false) { break; }
        $close = strpos($haystack, '[/av_one_third]', $start);
        if ($close === false) { break; }
        $end = $close + strlen('[/av_one_third]');
        $cards[] = [
            'start' => $start,
            'end'   => $end,
            'text'  => substr($haystack, $start, $end - $start),
        ];
        $cursor = $end;
    }
    if (count($cards) < 3) {
        return [
            'text'         => $haystack,
            'rewrote'      => false,
            'already_top'  => false,
        ];
    }

    // Idempotent: skip if the first card already links to this post.
    if (strpos($cards[0]['text'], "link='manually," . $new_link . "'") !== false) {
        return [
            'text'         => $haystack,
            'rewrote'      => false,
            'already_top'  => true,
        ];
    }

    // Substitutions are scoped to [av_image ...] because [av_one_third] also has empty src=/attachment= attrs.
    $new_card = $cards[0]['text'];
    if (preg_match('#\[av_image\b[^\]]*\]#u', $new_card, $img_m, PREG_OFFSET_CAPTURE)) {
        $img_open = $img_m[0][0];
        $img_pos  = $img_m[0][1];

        $new_img_open = $img_open;
        $new_img_open = preg_replace("#\\bsrc='[^']*'#u",
            "src='" . $new_image_url . "'", $new_img_open, 1);
        $new_img_open = preg_replace("#\\battachment='[^']*'#u",
            "attachment='" . $new_image_id . "'", $new_img_open, 1);
        $new_img_open = preg_replace("#\\blink='manually,[^']*'#u",
            "link='manually," . $new_link . "'", $new_img_open, 1);

        $new_card = substr($new_card, 0, $img_pos)
                  . $new_img_open
                  . substr($new_card, $img_pos + strlen($img_open));
    }
    // <h4> appears once per card (inside the [av_textblock]); safe to do unscoped.
    $new_card = preg_replace('#<h4 style="text-align: center;">[\s\S]*?</h4>#u',
        '<h4 style="text-align: center;">' . $new_title . '</h4>', $new_card, 1);

    // TEST MODE (append-only): keep all existing cards, just insert the new one on top.
    // The previous first card loses its `first` flag (only one card can be first).
    $demoted_first = preg_replace('#\[av_one_third first\b#u', '[av_one_third', $cards[0]['text'], 1);
    $sep01 = substr($haystack, $cards[0]['end'], $cards[1]['start'] - $cards[0]['end']);

    $prefix       = substr($haystack, 0, $cards[0]['start']);
    $suffix       = substr($haystack, $cards[0]['end']);
    $new_haystack = $prefix . $new_card . $sep01 . $demoted_first . $suffix;

    return [
        'text'         => $new_haystack,
        'rewrote'      => true,
        'already_top'  => false,
    ];
}

// --- reconcile (top-3 rebuild, used on delete & startup) -----------------

function eitaa_bridge_reconcile_news_section(WP_REST_Request $req) {
    $page_id       = (int) $req->get_param('page_id');
    $category_slug = trim((string) $req->get_param('category_slug'));
    $heading_text  = trim((string) $req->get_param('heading_text'));

    $page = get_post($page_id);
    if (!$page) {
        return new WP_Error('eitaa_bridge_not_found',
            sprintf('page %d not found', $page_id),
            ['status' => 404]);
    }
    if ($category_slug === '') {
        return new WP_Error('eitaa_bridge_bad_category',
            'category_slug must be non-empty',
            ['status' => 400]);
    }
    if ($heading_text === '') {
        return new WP_Error('eitaa_bridge_bad_heading',
            'heading_text must be non-empty',
            ['status' => 400]);
    }

    // Try by slug first (handles ASCII like "akhbar-etelaiyeh" and Persian like "اخبار-اطلاعیه").
    // Fall back to lookup by name so callers can pass the human-readable Persian label instead.
    $cat = get_category_by_slug($category_slug);
    if (!$cat) {
        $cat = get_term_by('name', $category_slug, 'category');
    }
    if (!$cat) {
        return new WP_Error('eitaa_bridge_bad_category',
            sprintf('category %s not found (tried slug and name)', $category_slug),
            ['status' => 400]);
    }

    // Fetch a wider window than 3 so that, when some recent posts lack a featured image, we can keep digging until we
    // either fill three card slots or exhaust the category. The bridge wants three cards on the homepage, not three of
    // the *newest* posts regardless of completeness.
    $posts = get_posts([
        'category'    => $cat->term_id,
        'post_status' => 'publish',
        'numberposts' => 30,
        'orderby'     => 'date',
        'order'       => 'DESC',
    ]);

    // Build {title, link, thumb_id, thumb_url} for each candidate post; skip ones missing a featured image.
    $infos = [];
    foreach ($posts as $p) {
        $thumb_id  = (int) get_post_thumbnail_id($p->ID);
        $thumb_url = $thumb_id > 0 ? (string) wp_get_attachment_url($thumb_id) : '';
        if ($thumb_id <= 0 || $thumb_url === '') {
            continue;
        }
        $title = (string) get_the_title($p->ID);
        $link  = (string) get_permalink($p->ID);
        if ($title === '' || $link === '') {
            continue;
        }
        // Single-quote in URL would corrupt the av_image shortcode attribute; reject these posts upstream.
        if (strpos($link, "'") !== false || strpos($thumb_url, "'") !== false) {
            continue;
        }
        $infos[] = [
            'title'     => $title,
            'link'      => $link,
            'thumb_id'  => $thumb_id,
            'thumb_url' => $thumb_url,
        ];
        if (count($infos) >= 3) { break; }
    }
    if (count($infos) === 0) {
        return new WP_Error('eitaa_bridge_no_complete_posts',
            sprintf('no publishable posts with featured image found in category %s', $category_slug),
            ['status' => 400]);
    }

    $touched = [];
    foreach (['_aviaLayoutBuilderCleanData', 'post_content'] as $field) {
        $haystack = $field === 'post_content'
            ? (string) $page->post_content
            : (string) get_post_meta($page_id, $field, true);
        if ($haystack === '') {
            continue;
        }
        $result = eitaa_bridge_reconcile_news_in_text($haystack, $heading_text, $infos);
        if (!$result['rewrote']) {
            continue;
        }
        if ($field === 'post_content') {
            wp_update_post(['ID' => $page_id, 'post_content' => $result['text']], true);
        } else {
            update_post_meta($page_id, $field, $result['text']);
        }
        $touched[] = $field;
    }

    if (count($touched) === 0) {
        return new WP_Error('eitaa_bridge_section_not_found',
            sprintf('no news section with heading %s found on page %d', $heading_text, $page_id),
            ['status' => 404]);
    }

    $caches = eitaa_bridge_bust_caches($page_id);

    $shown = [];
    foreach ($infos as $info) {
        $shown[] = ['title' => $info['title'], 'link' => $info['link']];
    }
    return [
        'page_id'        => $page_id,
        'category_slug'  => $category_slug,
        'reconciled'     => true,
        'card_count'     => count($infos),
        'cards'          => $shown,
        'touched'        => $touched,
        'cleared_caches' => $caches['cleared_caches'],
        'cache_actions'  => $caches['cache_actions'],
    ];
}

// Pure-string reconciliation: replace the existing up-to-three [av_one_third] cards under the heading
// with N freshly-built ones. Uses the first existing card as a template so the widget structure carries over.
function eitaa_bridge_reconcile_news_in_text(string $haystack, string $heading_text, array $infos) {
    $needle = "heading='" . $heading_text . "'";
    $h_pos  = strpos($haystack, $needle);
    if ($h_pos === false) {
        return ['text' => $haystack, 'rewrote' => false];
    }

    // Collect up to three existing [av_one_third] cards immediately following the heading.
    $cards = [];
    $cursor = $h_pos;
    for ($i = 0; $i < 3; $i++) {
        $start = strpos($haystack, '[av_one_third', $cursor);
        if ($start === false) { break; }
        $close = strpos($haystack, '[/av_one_third]', $start);
        if ($close === false) { break; }
        $end = $close + strlen('[/av_one_third]');
        $cards[] = ['start' => $start, 'end' => $end, 'text' => substr($haystack, $start, $end - $start)];
        $cursor = $end;
    }
    if (count($cards) === 0) {
        // No template card to clone — bail rather than guess the widget layout.
        return ['text' => $haystack, 'rewrote' => false];
    }

    // Idempotence: if existing cards already match $infos (same count, same links in order), skip.
    if (count($cards) === count($infos)) {
        $all_match = true;
        foreach ($infos as $i => $info) {
            if (strpos($cards[$i]['text'], "link='manually," . $info['link'] . "'") === false) {
                $all_match = false;
                break;
            }
        }
        if ($all_match) {
            return ['text' => $haystack, 'rewrote' => false];
        }
    }

    $template = $cards[0]['text'];
    // Separator between cards: preserve the existing one if two cards already exist, else default to a blank line.
    $sep = count($cards) > 1
        ? substr($haystack, $cards[0]['end'], $cards[1]['start'] - $cards[0]['end'])
        : "\n\n";

    $new_cards = [];
    foreach ($infos as $i => $info) {
        $card = $template;
        if ($i === 0) {
            // Ensure the first card carries the `first` flag (Avia uses it for left-edge styling).
            if (strpos($card, '[av_one_third first') === false) {
                $card = preg_replace('#\[av_one_third\b#u', '[av_one_third first', $card, 1);
            }
        } else {
            $card = preg_replace('#\[av_one_third first\b#u', '[av_one_third', $card, 1);
        }
        // Rewrite [av_image ...] attributes (scoped so we don't touch empty src/attachment elsewhere in the card).
        if (preg_match('#\[av_image\b[^\]]*\]#u', $card, $img_m, PREG_OFFSET_CAPTURE)) {
            $img_open = $img_m[0][0];
            $img_pos  = $img_m[0][1];
            $new_img  = $img_open;
            $new_img = preg_replace("#\\bsrc='[^']*'#u",
                "src='" . $info['thumb_url'] . "'", $new_img, 1);
            $new_img = preg_replace("#\\battachment='[^']*'#u",
                "attachment='" . (int) $info['thumb_id'] . "'", $new_img, 1);
            $new_img = preg_replace("#\\blink='manually,[^']*'#u",
                "link='manually," . $info['link'] . "'", $new_img, 1);
            $card = substr($card, 0, $img_pos) . $new_img . substr($card, $img_pos + strlen($img_open));
        }
        // Rewrite the <h4> title inside the card (one per card by convention).
        $card = preg_replace('#<h4 style="text-align: center;">[\s\S]*?</h4>#u',
            '<h4 style="text-align: center;">' . esc_html($info['title']) . '</h4>',
            $card, 1);
        $new_cards[] = $card;
    }

    $first_start = $cards[0]['start'];
    $last_end    = $cards[count($cards) - 1]['end'];
    $new_section = implode($sep, $new_cards);
    $new_text    = substr($haystack, 0, $first_start) . $new_section . substr($haystack, $last_end);

    return ['text' => $new_text, 'rewrote' => true];
}
