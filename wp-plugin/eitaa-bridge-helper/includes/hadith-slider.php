<?php
// Add and remove [av_content_slide] entries inside the homepage [av_content_slider] (the hadith / text-only slider).

if (!defined('ABSPATH')) {
    exit;
}

add_action('rest_api_init', function () {
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
});

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

    $caches = eitaa_bridge_bust_caches($page_id);

    return [
        'page_id'        => $page_id,
        'slide_uid'      => $uid,
        'touched'        => $touched,
        'cleared_caches' => $caches['cleared_caches'],
        'cache_actions'  => $caches['cache_actions'],
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

    if ($touched === []) {
        return new WP_Error('eitaa_bridge_uid_not_found',
            sprintf('no av_content_slide with av_uid=%s found', $uid),
            ['status' => 404]);
    }

    $caches = eitaa_bridge_bust_caches($page_id);

    return [
        'page_id'        => $page_id,
        'removed_uid'    => $uid,
        'touched'        => $touched,
        'cleared_caches' => $caches['cleared_caches'],
        'cache_actions'  => $caches['cache_actions'],
    ];
}
