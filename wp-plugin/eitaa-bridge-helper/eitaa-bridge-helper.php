<?php
/**
 * Plugin Name: Eitaa Bridge Helper
 * Description: REST endpoints that clone posts (preserving meta) and edit Avia layout for the eitaa-channel-bridge bot.
 * Version:     1.7.0
 * Author:      Mohsen
 */

if (!defined('ABSPATH')) {
    exit;
}

add_action('rest_api_init', function () {
    register_rest_route('eitaa-bridge/v1', '/clone-post', [
        'methods'             => 'POST',
        'permission_callback' => function () {
            return current_user_can('edit_posts');
        },
        'callback' => 'eitaa_bridge_clone_post',
        'args'     => [
            'source_id'      => ['required' => true, 'type' => 'integer'],
            'title'          => ['required' => true, 'type' => 'string'],
            'content'        => ['required' => true, 'type' => 'string'],
            'status'         => ['type' => 'string', 'default' => 'draft'],
            'category'       => ['type' => 'integer'],
            'featured_media' => ['type' => 'integer'],
            'slug'           => ['type' => 'string'],
        ],
    ]);

    // Diagnostic: dump every post_meta key+value (truncated) — used to find where Enfold/Avia stores layout.
    register_rest_route('eitaa-bridge/v1', '/page-meta', [
        'methods'             => 'GET',
        'permission_callback' => function () {
            return current_user_can('edit_pages');
        },
        'callback' => 'eitaa_bridge_page_meta',
        'args'     => [
            'page_id' => ['required' => true, 'type' => 'integer'],
        ],
    ]);

    // Add a slide to [av_content_slider]; writes CleanData + post_content and busts the parsed-tree + WP Rocket caches.
    register_rest_route('eitaa-bridge/v1', '/add-hadith-slide', [
        'methods'             => 'POST',
        'permission_callback' => function () {
            return current_user_can('edit_pages');
        },
        'callback' => 'eitaa_bridge_add_hadith_slide',
        'args'     => [
            'page_id' => ['required' => true, 'type' => 'integer'],
            'title'   => ['required' => true, 'type' => 'string'],
            'arabic'  => ['required' => true, 'type' => 'string'],
            'persian' => ['required' => true, 'type' => 'string'],
            'source'  => ['required' => true, 'type' => 'string'],
        ],
    ]);

    // Remove an av_content_slide by av_uid from CleanData + post_content; symmetric to add-hadith-slide.
    register_rest_route('eitaa-bridge/v1', '/remove-slide', [
        'methods'             => 'POST',
        'permission_callback' => function () {
            return current_user_can('edit_pages');
        },
        'callback' => 'eitaa_bridge_remove_slide',
        'args'     => [
            'page_id' => ['required' => true, 'type' => 'integer'],
            'uid'     => ['required' => true, 'type' => 'string'],
        ],
    ]);

    // Diagnostic: list every [av_slide] / [av_content_slide] on a page with av_uid, link, image id.
    register_rest_route('eitaa-bridge/v1', '/list-slides', [
        'methods'             => 'GET',
        'permission_callback' => function () {
            return current_user_can('edit_pages');
        },
        'callback' => 'eitaa_bridge_list_slides',
        'args'     => [
            'page_id' => ['required' => true, 'type' => 'integer'],
        ],
    ]);

    // Return WP front-page settings so the bridge can auto-discover the home page id.
    register_rest_route('eitaa-bridge/v1', '/site-settings', [
        'methods'             => 'GET',
        'permission_callback' => function () {
            return current_user_can('edit_pages');
        },
        'callback' => 'eitaa_bridge_site_settings',
    ]);

    // Diagnostic: dump the raw shortcodes for the news-cards section bounded by a heading
    // text marker, so we can see the real Avia/WPBakery source before writing rotation logic.
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

    // Rotate the three news cards on the homepage: a fresh card (cloned from the current first card with
    // its image / title / link replaced) goes on top, the previous first/second cards shift down, the
    // previous third card is dropped. Idempotent: if the current first card already points at this
    // post's permalink, nothing changes.
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

    // Rewrite an [av_slide] link attribute by av_uid so the homepage poster always points to the latest report.
    register_rest_route('eitaa-bridge/v1', '/update-slide-link', [
        'methods'             => 'POST',
        'permission_callback' => function () {
            return current_user_can('edit_pages');
        },
        'callback' => 'eitaa_bridge_update_slide_link',
        'args'     => [
            'page_id' => ['required' => true, 'type' => 'integer'],
            'uid'     => ['required' => true, 'type' => 'string'],
            'link'    => ['required' => true, 'type' => 'string'],
        ],
    ]);
});

function eitaa_bridge_page_meta(WP_REST_Request $req) {
    $page_id = (int) $req->get_param('page_id');
    $key     = (string) $req->get_param('key');
    if (!get_post($page_id)) {
        return new WP_Error('eitaa_bridge_not_found',
            sprintf('page %d not found', $page_id),
            ['status' => 404]);
    }

    // Single-key, no-truncate mode: useful when we already know which key to inspect.
    if ($key !== '') {
        $vals = get_post_meta($page_id, $key);
        return [
            'page_id' => $page_id,
            'key'     => $key,
            'values'  => array_map(function ($v) { return (string) $v; }, $vals),
            'sizes'   => array_map(function ($v) { return strlen((string) $v); }, $vals),
        ];
    }

    $meta = get_post_meta($page_id);
    $summary = [];
    foreach ($meta as $k => $values) {
        $vals = [];
        foreach ($values as $v) {
            $s = is_string($v) ? $v : (string) $v;
            if (strlen($s) > 2000) {
                $vals[] = substr($s, 0, 2000) . '...[truncated; total ' . strlen($s) . ' bytes]';
            } else {
                $vals[] = $s;
            }
        }
        $summary[$k] = $vals;
    }
    $avia = [];
    if (class_exists('AviaBuilder'))      { $avia[] = 'AviaBuilder class present'; }
    if (defined('AVIA_FW'))               { $avia[] = 'AVIA_FW defined'; }
    if (function_exists('avia_post_meta')){ $avia[] = 'avia_post_meta() present'; }
    if (function_exists('rocket_clean_post')) { $avia[] = 'WP Rocket present (rocket_clean_post)'; }
    return [
        'page_id'        => $page_id,
        'meta_key_count' => count($meta),
        'meta_keys'      => array_keys($meta),
        'meta_values'    => $summary,
        'avia_runtime'   => $avia,
    ];
}

function eitaa_bridge_add_hadith_slide(WP_REST_Request $req) {
    $page_id = (int) $req->get_param('page_id');
    $page    = get_post($page_id);
    if (!$page) {
        return new WP_Error('eitaa_bridge_not_found',
            sprintf('page %d not found', $page_id),
            ['status' => 404]);
    }

    $title   = (string) $req->get_param('title');
    $arabic  = (string) $req->get_param('arabic');
    $persian = (string) $req->get_param('persian');
    $source  = (string) $req->get_param('source');
    $uid     = 'av-' . substr(md5(uniqid('', true)), 0, 4);
    $closer  = '[/av_content_slider]';

    // _aviaLayoutBuilderCleanData is the canonical layout source; post_content is only a display cache, so update CleanData first.
    $clean_data = get_post_meta($page_id, '_aviaLayoutBuilderCleanData', true);
    $touched = [];

    if (is_string($clean_data) && $clean_data !== '') {
        $idx_cd = strpos($clean_data, $closer);
        if ($idx_cd === false) {
            return new WP_Error('eitaa_bridge_no_slider_cleandata',
                'no [av_content_slider] block inside _aviaLayoutBuilderCleanData',
                ['status' => 400]);
        }
        // CleanData uses \r\n line endings — match them so the diff stays clean.
        $slide_crlf = "[av_content_slide title='{$title}' heading_tag='' heading_class='' link='' linktarget='' av_uid='{$uid}']\r\n"
                    . "<p style=\"text-align: center;\"><strong>{$arabic}</strong>\r\n{$persian}\r\n{$source}</p>\r\n"
                    . "[/av_content_slide]\r\n";
        $new_clean = substr($clean_data, 0, $idx_cd) . $slide_crlf . substr($clean_data, $idx_cd);
        update_post_meta($page_id, '_aviaLayoutBuilderCleanData', $new_clean);
        $touched[] = '_aviaLayoutBuilderCleanData';
    }

    // Keep post_content in sync; the shortcode parser tree (_avia_builder_shortcode_tree) is rebuilt from it on save_post.
    $idx_pc = strpos($page->post_content, $closer);
    if ($idx_pc === false) {
        return new WP_Error('eitaa_bridge_no_slider',
            'no [av_content_slider] block in post_content',
            ['status' => 400]);
    }
    $slide_lf = "[av_content_slide title='{$title}' heading_tag='' heading_class='' link='' linktarget='' av_uid='{$uid}']\n"
              . "<p style=\"text-align: center;\"><strong>{$arabic}</strong>\n{$persian}\n{$source}</p>\n"
              . "[/av_content_slide]\n";
    $new_content = substr($page->post_content, 0, $idx_pc) . $slide_lf . substr($page->post_content, $idx_pc);
    $result = wp_update_post([
        'ID'           => $page_id,
        'post_content' => $new_content,
    ], true);
    if (is_wp_error($result)) {
        return $result;
    }
    $touched[] = 'post_content';

    // Force Avia to re-parse the shortcode tree next request by dropping the cached version.
    $cleared = [];
    foreach (['_avia_builder_shortcode_tree', '_avia_sc_parser_state'] as $k) {
        if (metadata_exists('post', $page_id, $k)) {
            delete_post_meta($page_id, $k);
            $cleared[] = $k;
        }
    }

    // WP Rocket page cache: clear so visitors see the new HTML.
    $cache_actions = [];
    if (function_exists('rocket_clean_post')) {
        rocket_clean_post($page_id);
        $cache_actions[] = 'rocket_clean_post';
    }
    if (function_exists('rocket_clean_home')) {
        rocket_clean_home();
        $cache_actions[] = 'rocket_clean_home';
    }
    // Generic WP object cache flush (safe; rebuild is cheap).
    wp_cache_delete($page_id, 'posts');
    wp_cache_delete($page_id, 'post_meta');
    clean_post_cache($page_id);
    $cache_actions[] = 'clean_post_cache';

    return [
        'page_id'        => $page_id,
        'slide_uid'      => $uid,
        'touched'        => $touched,
        'cleared_caches' => $cleared,
        'cache_actions'  => $cache_actions,
    ];
}

function eitaa_bridge_remove_slide(WP_REST_Request $req) {
    $page_id = (int) $req->get_param('page_id');
    $uid     = (string) $req->get_param('uid');
    $page    = get_post($page_id);
    if (!$page) {
        return new WP_Error('eitaa_bridge_not_found',
            sprintf('page %d not found', $page_id),
            ['status' => 404]);
    }
    if ($uid === '' || strpos($uid, "'") !== false) {
        return new WP_Error('eitaa_bridge_bad_uid',
            'uid is empty or contains illegal characters',
            ['status' => 400]);
    }

    // Match the whole slide block by av_uid across either line-ending convention.
    $pattern = '#\[av_content_slide[^\]]*av_uid=\'' . preg_quote($uid, '#') . '\'[^\]]*\][\s\S]*?\[/av_content_slide\]\r?\n?#u';

    $touched = [];
    $clean_data = get_post_meta($page_id, '_aviaLayoutBuilderCleanData', true);
    if (is_string($clean_data) && $clean_data !== '') {
        $new_clean = preg_replace($pattern, '', $clean_data, 1, $cnt);
        if ($cnt > 0) {
            update_post_meta($page_id, '_aviaLayoutBuilderCleanData', $new_clean);
            $touched[] = '_aviaLayoutBuilderCleanData';
        }
    }
    $new_content = preg_replace($pattern, '', $page->post_content, 1, $cnt2);
    if ($cnt2 > 0) {
        wp_update_post(['ID' => $page_id, 'post_content' => $new_content], true);
        $touched[] = 'post_content';
    }

    $cleared = [];
    foreach (['_avia_builder_shortcode_tree', '_avia_sc_parser_state'] as $k) {
        if (metadata_exists('post', $page_id, $k)) {
            delete_post_meta($page_id, $k);
            $cleared[] = $k;
        }
    }
    $cache_actions = [];
    if (function_exists('rocket_clean_post')) { rocket_clean_post($page_id); $cache_actions[] = 'rocket_clean_post'; }
    if (function_exists('rocket_clean_home')) { rocket_clean_home();         $cache_actions[] = 'rocket_clean_home'; }
    clean_post_cache($page_id);
    $cache_actions[] = 'clean_post_cache';

    if ($touched === []) {
        return new WP_Error('eitaa_bridge_uid_not_found',
            sprintf('no av_content_slide with av_uid=%s found', $uid),
            ['status' => 404]);
    }
    return [
        'page_id'        => $page_id,
        'removed_uid'    => $uid,
        'touched'        => $touched,
        'cleared_caches' => $cleared,
        'cache_actions'  => $cache_actions,
    ];
}

function eitaa_bridge_clone_post(WP_REST_Request $req) {
    $source_id      = (int) $req->get_param('source_id');
    $title          = (string) $req->get_param('title');
    $content        = (string) $req->get_param('content');
    $status         = $req->get_param('status') ?: 'draft';
    $category_id    = (int) $req->get_param('category');
    $featured_media = (int) $req->get_param('featured_media');
    $slug           = sanitize_title((string) $req->get_param('slug'));

    $source = get_post($source_id);
    if (!$source) {
        return new WP_Error('eitaa_bridge_not_found',
            sprintf('source post %d not found', $source_id),
            ['status' => 404]);
    }

    $insert = [
        'post_title'     => $title,
        'post_content'   => $content,
        'post_status'    => $status,
        'post_type'      => $source->post_type,
        'post_author'    => get_current_user_id(),
        'post_excerpt'   => $source->post_excerpt,
        'post_password'  => $source->post_password,
        'comment_status' => $source->comment_status,
        'ping_status'    => $source->ping_status,
        'menu_order'     => $source->menu_order,
        'post_parent'    => $source->post_parent,
    ];
    if ($slug !== '') {
        $insert['post_name'] = $slug;
    }
    $new_id = wp_insert_post($insert, true);
    if (is_wp_error($new_id)) {
        return $new_id;
    }

    // Copy every post_meta from the source — what standard REST can't do; lets the page builder + theme layout work.
    $skip = [
        '_edit_lock', '_edit_last',
        '_thumbnail_id',     // featured image handled below
        '_yoast_wpseo_focuskw', '_yoast_wpseo_metadesc', '_yoast_wpseo_title',
    ];
    $meta = get_post_meta($source_id);
    foreach ($meta as $key => $values) {
        if (in_array($key, $skip, true)) {
            continue;
        }
        foreach ($values as $value) {
            add_post_meta($new_id, $key, maybe_unserialize($value));
        }
    }

    // Featured image: prefer the passed-in one, else the source's, so the new post is never imageless.
    if ($featured_media > 0) {
        set_post_thumbnail($new_id, $featured_media);
    } elseif ($src_thumb = get_post_thumbnail_id($source_id)) {
        set_post_thumbnail($new_id, $src_thumb);
    }

    // Category: override if given, otherwise inherit the source's.
    if ($category_id > 0) {
        wp_set_post_categories($new_id, [$category_id]);
    } else {
        wp_set_post_categories($new_id, wp_get_post_categories($source_id));
    }

    // Post format (used by some themes to switch layout).
    if ($format = get_post_format($source_id)) {
        set_post_format($new_id, $format);
    }

    return [
        'id'        => $new_id,
        'link'      => get_permalink($new_id),
        'source_id' => $source_id,
        'status'    => $status,
    ];
}

function eitaa_bridge_site_settings(WP_REST_Request $req) {
    return [
        'show_on_front'  => (string) get_option('show_on_front'),
        'page_on_front'  => (int) get_option('page_on_front'),
        'page_for_posts' => (int) get_option('page_for_posts'),
    ];
}

function eitaa_bridge_list_slides(WP_REST_Request $req) {
    $page_id = (int) $req->get_param('page_id');
    $page    = get_post($page_id);
    if (!$page) {
        return new WP_Error('eitaa_bridge_not_found',
            sprintf('page %d not found', $page_id),
            ['status' => 404]);
    }
    // Prefer the canonical Avia source if present, otherwise read post_content.
    $haystack = (string) get_post_meta($page_id, '_aviaLayoutBuilderCleanData', true);
    if ($haystack === '') {
        $haystack = (string) $page->post_content;
    }
    $out = [];
    // av_slide: image slideshow entry; \b avoids matching the parent [av_slideshow] widget tag.
    if (preg_match_all('#\[av_slide\b([^\]]*)\][\s\S]*?\[/av_slide\]#u', $haystack, $blocks, PREG_SET_ORDER)) {
        foreach ($blocks as $b) {
            $attrs = $b[1];
            $out[] = [
                'type'     => 'av_slide',
                'uid'      => eitaa_bridge_attr($attrs, 'av_uid'),
                'image_id' => eitaa_bridge_attr($attrs, 'id'),
                'link'     => eitaa_bridge_attr($attrs, 'link'),
                'title'    => eitaa_bridge_attr($attrs, 'title'),
            ];
        }
    }
    // av_content_slide: hadith-style text slider entry; \b avoids matching the parent [av_content_slider] widget tag.
    if (preg_match_all('#\[av_content_slide\b([^\]]*)\][\s\S]*?\[/av_content_slide\]#u', $haystack, $blocks, PREG_SET_ORDER)) {
        foreach ($blocks as $b) {
            $attrs = $b[1];
            $out[] = [
                'type'  => 'av_content_slide',
                'uid'   => eitaa_bridge_attr($attrs, 'av_uid'),
                'title' => eitaa_bridge_attr($attrs, 'title'),
            ];
        }
    }
    return ['page_id' => $page_id, 'slides' => $out, 'count' => count($out)];
}

// rotate-news-section is the main news-cards rotator. Algorithm:
//   1. Locate the heading by exact text match.
//   2. Capture the next three [av_one_third] ... [/av_one_third] blocks.
//   3. Build a NEW card by cloning cards[0] (the current first card) and substituting
//      its image src / attachment id / link / <h4> title with the new post's values.
//   4. Demote the old cards[0] by removing its `first` attribute.
//   5. Reassemble: new_card + sep + old_first_demoted + sep + cards[1].
//      cards[2] (oldest) and its preceding separator are dropped.
//   6. Apply to both _aviaLayoutBuilderCleanData and post_content, bust caches.
//
// Idempotent: if cards[0]'s link already equals the new post's permalink, the section is left untouched.
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

    // Resolve the new card's content from the post.
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

    // Bust caches identical to the other Avia-editing endpoints.
    $cleared = [];
    foreach (['_avia_builder_shortcode_tree', '_avia_sc_parser_state'] as $k) {
        if (metadata_exists('post', $page_id, $k)) {
            delete_post_meta($page_id, $k);
            $cleared[] = $k;
        }
    }
    $cache_actions = [];
    if (function_exists('rocket_clean_post')) { rocket_clean_post($page_id); $cache_actions[] = 'rocket_clean_post'; }
    if (function_exists('rocket_clean_home')) { rocket_clean_home();         $cache_actions[] = 'rocket_clean_home'; }
    clean_post_cache($page_id);
    $cache_actions[] = 'clean_post_cache';

    return [
        'page_id'        => $page_id,
        'post_id'        => $post_id,
        'rotated'        => true,
        'touched'        => $touched,
        'cleared_caches' => $cleared,
        'cache_actions'  => $cache_actions,
        'new_title'      => $post_title,
        'new_link'       => $post_link,
        'new_image_id'   => $thumb_id,
    ];
}

// eitaa_bridge_rotate_news_in_text is the pure-string rotation logic, isolated so the same code
// runs against both CleanData and post_content without touching WP globals. It returns:
//   - text:         the rewritten haystack (only meaningful when rewrote=true)
//   - rewrote:      true when the haystack changed
//   - already_top:  true when the first card already points at $new_link (no change needed)
//   - or a WP_Error if the section / cards couldn't be located.
function eitaa_bridge_rotate_news_in_text(string $haystack, string $heading_text,
                                          string $new_link, string $new_image_url, int $new_image_id,
                                          string $new_title) {
    // Anchor: the heading shortcode that opens the news section.
    $needle = "heading='" . $heading_text . "'";
    $h_pos  = strpos($haystack, $needle);
    if ($h_pos === false) {
        return [
            'text'         => $haystack,
            'rewrote'      => false,
            'already_top'  => false,
        ];
    }

    // Capture the next three [av_one_third]...[/av_one_third] blocks after the heading.
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

    // Idempotency check: the first card already links to this post → no rotation needed.
    if (strpos($cards[0]['text'], "link='manually," . $new_link . "'") !== false) {
        return [
            'text'         => $haystack,
            'rewrote'      => false,
            'already_top'  => true,
        ];
    }

    // Build the new top card by cloning cards[0] and substituting image / link / title.
    // The src= and attachment= attributes also appear (empty) on the outer [av_one_third]
    // column shortcode, so we must scope the image edits to the [av_image ...] opening tag
    // only — otherwise we'd overwrite the column background instead of the image.
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

    // Demote the old first card by stripping the `first` flag.
    $demoted_first = preg_replace('#\[av_one_third first\b#u', '[av_one_third', $cards[0]['text'], 1);

    // Capture the separator between original cards[0] and cards[1] (newlines/whitespace).
    $sep01 = substr($haystack, $cards[0]['end'], $cards[1]['start'] - $cards[0]['end']);

    // Reassemble: prefix + new_card + sep + demoted_first + sep + cards[1] + suffix (after cards[2]).
    $prefix       = substr($haystack, 0, $cards[0]['start']);
    $suffix       = substr($haystack, $cards[2]['end']);
    $new_section  = $new_card . $sep01 . $demoted_first . $sep01 . $cards[1]['text'];
    $new_haystack = $prefix . $new_section . $suffix;

    return [
        'text'         => $new_haystack,
        'rewrote'      => true,
        'already_top'  => false,
    ];
}

// news-section-source returns the shortcode bytes between the section heading and the next heading
// so we can see how the homepage cards are actually authored before writing rotation logic against them.
// Both sources of truth are returned: the canonical _aviaLayoutBuilderCleanData and the displayed post_content.
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

// eitaa_bridge_news_extract_section finds the substring after the heading text marker
// up to the next [av_heading] or [av_special_heading] shortcode (the next section).
// It returns the heading offset, the section bytes, and the next-heading offset for inspection.
function eitaa_bridge_news_extract_section(string $haystack, string $heading_text): array {
    $heading_pos = mb_strpos($haystack, $heading_text);
    if ($heading_pos === false) {
        return [
            'heading_found' => false,
            'note'          => 'heading_text not found anywhere in this source',
        ];
    }
    // Convert mb char offset back to byte offset for substr().
    $byte_pos = strlen(mb_substr($haystack, 0, $heading_pos));

    // Section ends at the next [av_heading or [av_special_heading shortcode opener.
    $rest = substr($haystack, $byte_pos);
    $end_offset = strlen($rest); // default: rest of document
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

// Pulls a single='quoted' attribute value out of an Avia shortcode attribute string.
function eitaa_bridge_attr(string $attrs, string $name): string {
    if (preg_match("#\\b" . preg_quote($name, '#') . "='([^']*)'#u", $attrs, $m)) {
        return $m[1];
    }
    return '';
}

function eitaa_bridge_update_slide_link(WP_REST_Request $req) {
    $page_id = (int) $req->get_param('page_id');
    $uid     = (string) $req->get_param('uid');
    $link    = (string) $req->get_param('link');
    $page    = get_post($page_id);
    if (!$page) {
        return new WP_Error('eitaa_bridge_not_found',
            sprintf('page %d not found', $page_id),
            ['status' => 404]);
    }
    if ($uid === '' || strpos($uid, "'") !== false) {
        return new WP_Error('eitaa_bridge_bad_uid',
            'uid is empty or contains illegal characters',
            ['status' => 400]);
    }
    if (strpos($link, "'") !== false) {
        return new WP_Error('eitaa_bridge_bad_link',
            "link must not contain a single quote",
            ['status' => 400]);
    }

    // Avia stores image-slide links as link='manually,<url>'; build that verbatim so other styles (e.g. 'lightbox') aren't disturbed.
    $new_link_val = $link === '' ? '' : 'manually,' . $link;
    // Rewrite link='...' inside [av_slide ...] blocks whose av_uid matches.
    $rewriter = function ($block) use ($uid, $new_link_val) {
        $attrs = $block[1];
        if (!preg_match("#av_uid='" . preg_quote($uid, '#') . "'#u", $attrs)) {
            return $block[0]; // not our slide — leave untouched
        }
        $newAttrs = preg_replace(
            "#\\blink='[^']*'#u",
            "link='" . $new_link_val . "'",
            $attrs,
            1
        );
        return '[av_slide' . $newAttrs . ']' . $block[2] . '[/av_slide]';
    };

    $touched = [];
    $clean_data = (string) get_post_meta($page_id, '_aviaLayoutBuilderCleanData', true);
    if ($clean_data !== '') {
        $hits = 0;
        $new_clean = preg_replace_callback(
            '#\[av_slide\b([^\]]*)\]([\s\S]*?)\[/av_slide\]#u',
            function ($m) use ($rewriter, &$hits) {
                $out = $rewriter($m);
                if ($out !== $m[0]) { $hits++; }
                return $out;
            },
            $clean_data
        );
        if ($hits > 0) {
            update_post_meta($page_id, '_aviaLayoutBuilderCleanData', $new_clean);
            $touched[] = '_aviaLayoutBuilderCleanData';
        }
    }
    $hits_pc = 0;
    $new_content = preg_replace_callback(
        '#\[av_slide\b([^\]]*)\]([\s\S]*?)\[/av_slide\]#u',
        function ($m) use ($rewriter, &$hits_pc) {
            $out = $rewriter($m);
            if ($out !== $m[0]) { $hits_pc++; }
            return $out;
        },
        $page->post_content
    );
    if ($hits_pc > 0) {
        wp_update_post(['ID' => $page_id, 'post_content' => $new_content], true);
        $touched[] = 'post_content';
    }
    if ($touched === []) {
        return new WP_Error('eitaa_bridge_uid_not_found',
            sprintf('no av_slide with av_uid=%s found on page %d', $uid, $page_id),
            ['status' => 404]);
    }

    // Bust the same caches as the slide add/remove endpoints.
    $cleared = [];
    foreach (['_avia_builder_shortcode_tree', '_avia_sc_parser_state'] as $k) {
        if (metadata_exists('post', $page_id, $k)) {
            delete_post_meta($page_id, $k);
            $cleared[] = $k;
        }
    }
    $cache_actions = [];
    if (function_exists('rocket_clean_post')) { rocket_clean_post($page_id); $cache_actions[] = 'rocket_clean_post'; }
    if (function_exists('rocket_clean_home')) { rocket_clean_home();         $cache_actions[] = 'rocket_clean_home'; }
    clean_post_cache($page_id);
    $cache_actions[] = 'clean_post_cache';

    return [
        'page_id'        => $page_id,
        'uid'            => $uid,
        'new_link'       => $new_link_val,
        'touched'        => $touched,
        'cleared_caches' => $cleared,
        'cache_actions'  => $cache_actions,
    ];
}
