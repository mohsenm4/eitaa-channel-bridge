<?php
// Read-only diagnostic endpoints used while developing & operating the bridge:
//   /page-meta     — dump all post_meta keys/values for a page
//   /list-slides   — list every [av_slide] and [av_content_slide] on a page
//   /site-settings — WP front-page settings so the bridge can auto-discover the home page id

if (!defined('ABSPATH')) {
    exit;
}

add_action('rest_api_init', function () {
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

    register_rest_route('eitaa-bridge/v1', '/site-settings', [
        'methods'             => 'GET',
        'permission_callback' => function () {
            return current_user_can('edit_pages');
        },
        'callback' => 'eitaa_bridge_site_settings',
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

function eitaa_bridge_site_settings(WP_REST_Request $req) {
    return [
        'show_on_front'  => (string) get_option('show_on_front'),
        'page_on_front'  => (int) get_option('page_on_front'),
        'page_for_posts' => (int) get_option('page_for_posts'),
    ];
}
