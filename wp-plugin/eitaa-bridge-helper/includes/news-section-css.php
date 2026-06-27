<?php
// Inject a small CSS rule on the front page that forces all three news-card images to share one aspect ratio.
// Authors upload featured images at whatever dimensions Eitaa hands them, so without this the cards render at
// each image's native ratio and the section looks uneven.
//
// Scoping note: we only target the static front page (so other [av_one_third] blocks site-wide stay untouched)
// and rely on object-fit:cover to crop without distortion.

if (!defined('ABSPATH')) {
    exit;
}

add_action('wp_head', function () {
    if (!is_front_page()) {
        return;
    }
    ?>
<style id="eitaa-bridge-news-card-uniformity">
    /* Force news-card featured images to a uniform 16:9 with center-crop on the static front page. */
    body.home .av_one_third .avia-image-container img,
    body.home .av_one_third .avia_image,
    body.home .av_one_third img.avia_image_link {
        aspect-ratio: 16 / 9;
        object-fit: cover;
        width: 100% !important;
        height: auto !important;
    }
</style>
<?php
});
