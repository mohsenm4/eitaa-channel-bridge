<?php
/**
 * Plugin Name: Eitaa Bridge Helper
 * Description: REST endpoints that clone posts (preserving meta) and edit Avia layout for the eitaa-channel-bridge bot.
 * Version:     1.8.4
 * Author:      Mohsen
 */

if (!defined('ABSPATH')) {
    exit;
}

// Shared helpers first — every endpoint file uses eitaa_bridge_bust_caches and eitaa_bridge_attr.
require_once __DIR__ . '/includes/util.php';

require_once __DIR__ . '/includes/clone-post.php';
require_once __DIR__ . '/includes/diagnostics.php';
require_once __DIR__ . '/includes/hadith-slider.php';
require_once __DIR__ . '/includes/poster-slider.php';
require_once __DIR__ . '/includes/news-section.php';
require_once __DIR__ . '/includes/news-section-css.php';
