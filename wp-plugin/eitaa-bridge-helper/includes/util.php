<?php
// Shared helpers used by every endpoint: cache invalidation + shortcode attribute extraction.

if (!defined('ABSPATH')) {
    exit;
}

// Drop the parsed shortcode tree, flush WP Rocket page caches, and clean the post cache after a layout edit.
// Returns the lists so callers can include them in their REST response for visibility.
function eitaa_bridge_bust_caches(int $page_id): array {
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
    return ['cleared_caches' => $cleared, 'cache_actions' => $cache_actions];
}

// Pulls a single='quoted' attribute value out of an Avia shortcode attribute string.
function eitaa_bridge_attr(string $attrs, string $name): string {
    if (preg_match("#\\b" . preg_quote($name, '#') . "='([^']*)'#u", $attrs, $m)) {
        return $m[1];
    }
    return '';
}
