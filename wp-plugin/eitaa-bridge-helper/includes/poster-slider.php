<?php
// /update-slide-link — rewrite the link='manually,...' attribute on a single [av_slide] (image-slider poster) by av_uid.

if (!defined('ABSPATH')) {
    exit;
}

add_action('rest_api_init', function () {
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

    $caches = eitaa_bridge_bust_caches($page_id);

    return [
        'page_id'        => $page_id,
        'uid'            => $uid,
        'new_link'       => $new_link_val,
        'touched'        => $touched,
        'cleared_caches' => $caches['cleared_caches'],
        'cache_actions'  => $caches['cache_actions'],
    ];
}
