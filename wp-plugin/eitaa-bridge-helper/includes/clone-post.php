<?php
// /clone-post — deep-copies a post (including all post_meta) so the new post keeps the page-builder layout.

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
});

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
