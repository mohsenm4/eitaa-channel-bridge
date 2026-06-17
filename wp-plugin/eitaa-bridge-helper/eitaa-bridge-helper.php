<?php
/**
 * Plugin Name: Eitaa Bridge Helper
 * Description: REST endpoints that clone posts (preserving meta) and edit Avia layout for the eitaa-channel-bridge bot.
 * Version:     1.5.0
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
