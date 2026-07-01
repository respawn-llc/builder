#[cfg(target_os = "macos")]
mod platform {
    use objc2::{
        runtime::{AnyClass, NSObjectProtocol},
    };
    use objc2_app_kit::{
        NSAutoresizingMaskOptions, NSColor, NSGlassEffectView, NSGlassEffectViewStyle,
        NSVisualEffectBlendingMode, NSVisualEffectMaterial, NSVisualEffectState,
        NSVisualEffectView, NSWindow,
    };
    use objc2_foundation::{NSOperatingSystemVersion, NSProcessInfo};
    use tauri::{Manager, Runtime, WebviewWindow};

    const LIQUID_GLASS_MAJOR_VERSION: isize = 26;
    const LIQUID_GLASS_EFFECT_NAME: &str = "NSGlassEffectView.regular";
    const VISUAL_EFFECT_NAME: &str = "NSVisualEffectView.underWindowBackground";
    const ISLAND_BASE_CORNER_RADIUS: f64 = 24.0;

    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    enum NativeWindowEffect {
        LiquidGlass,
        VisualEffect,
    }

    #[derive(serde::Serialize)]
    #[serde(rename_all = "camelCase", tag = "status")]
    pub enum NativeGlassStatus {
        Applied { effect: &'static str },
        Unsupported { reason: &'static str },
    }

    #[derive(Clone, Copy, serde::Deserialize)]
    #[serde(rename_all = "camelCase")]
    pub struct NativeGlassTint {
        red: f64,
        green: f64,
        blue: f64,
        alpha: f64,
    }

    pub async fn apply_to_label<R: Runtime>(
        app: tauri::AppHandle<R>,
        label: String,
    ) -> Result<NativeGlassStatus, String> {
        let window = app
            .get_webview_window(&label)
            .ok_or_else(|| format!("Window '{label}' was not found."))?;
        apply_to_window(window).await
    }

    pub async fn set_tint_for_label<R: Runtime>(
        app: tauri::AppHandle<R>,
        label: String,
        tint: Option<NativeGlassTint>,
    ) -> Result<NativeGlassStatus, String> {
        let window = app
            .get_webview_window(&label)
            .ok_or_else(|| format!("Window '{label}' was not found."))?;
        set_tint_for_window(window, tint).await
    }

    pub fn apply_to_window_now<R: Runtime>(
        window: &WebviewWindow<R>,
    ) -> Result<NativeGlassStatus, String> {
        let ns_window = window
            .ns_window()
            .map_err(|error| format!("Resolve native window failed: {error}"))?;
        unsafe { apply_to_ns_window(ns_window.cast()) }
    }

    async fn apply_to_window<R: Runtime>(
        window: WebviewWindow<R>,
    ) -> Result<NativeGlassStatus, String> {
        let (sender, receiver) = tokio::sync::oneshot::channel();
        let scheduled_window = window.clone();
        scheduled_window
            .run_on_main_thread(move || {
                let result = apply_to_window_now(&window);
                let _ = sender.send(result);
            })
            .map_err(|error| format!("Schedule native glass setup failed: {error}"))?;
        receiver
            .await
            .map_err(|_| "Native glass setup ended before returning a result.".to_string())?
    }

    async fn set_tint_for_window<R: Runtime>(
        window: WebviewWindow<R>,
        tint: Option<NativeGlassTint>,
    ) -> Result<NativeGlassStatus, String> {
        let (sender, receiver) = tokio::sync::oneshot::channel();
        let scheduled_window = window.clone();
        scheduled_window
            .run_on_main_thread(move || {
                let result = set_tint_for_window_now(&window, tint);
                let _ = sender.send(result);
            })
            .map_err(|error| format!("Schedule native glass tint update failed: {error}"))?;
        receiver
            .await
            .map_err(|_| "Native glass tint update ended before returning a result.".to_string())?
    }

    fn set_tint_for_window_now<R: Runtime>(
        window: &WebviewWindow<R>,
        tint: Option<NativeGlassTint>,
    ) -> Result<NativeGlassStatus, String> {
        let ns_window = window
            .ns_window()
            .map_err(|error| format!("Resolve native window failed: {error}"))?;
        unsafe { set_tint_for_ns_window(ns_window.cast(), tint) }
    }

    unsafe fn apply_to_ns_window(ns_window: *mut NSWindow) -> Result<NativeGlassStatus, String> {
        if ns_window.is_null() {
            return Err("Native window pointer is null.".to_string());
        }
        let glass_effect_class = AnyClass::get(c"NSGlassEffectView");
        let visual_effect_class = AnyClass::get(c"NSVisualEffectView");
        let Some(effect) = select_native_effect(
            NSProcessInfo::processInfo().operatingSystemVersion(),
            glass_effect_class.is_some(),
            visual_effect_class.is_some(),
        ) else {
            return Ok(NativeGlassStatus::Unsupported {
                reason: "Native macOS window blur is unavailable.",
            });
        };

        let window = unsafe { &*ns_window };
        let content_view = window
            .contentView()
            .ok_or_else(|| "Native window does not have a content view.".to_string())?;
        if let Some(glass_class) = glass_effect_class {
            if content_view.isKindOfClass(glass_class) {
                return Ok(NativeGlassStatus::Applied {
                    effect: LIQUID_GLASS_EFFECT_NAME,
                });
            }
        }
        if let Some(visual_effect_class) = visual_effect_class {
            if content_view.isKindOfClass(visual_effect_class) {
                return Ok(NativeGlassStatus::Applied {
                    effect: VISUAL_EFFECT_NAME,
                });
            }
        }

        let main_thread = objc2::MainThreadMarker::new()
            .ok_or_else(|| "Native glass setup must run on the main thread.".to_string())?;
        if effect == NativeWindowEffect::LiquidGlass {
            return Ok(apply_liquid_glass(window, &content_view, main_thread));
        }
        Ok(apply_visual_effect(window, &content_view, main_thread))
    }

    unsafe fn set_tint_for_ns_window(
        ns_window: *mut NSWindow,
        tint: Option<NativeGlassTint>,
    ) -> Result<NativeGlassStatus, String> {
        if ns_window.is_null() {
            return Err("Native window pointer is null.".to_string());
        }
        let window = unsafe { &*ns_window };
        let content_view = window
            .contentView()
            .ok_or_else(|| "Native window does not have a content view.".to_string())?;
        if AnyClass::get(c"NSGlassEffectView").is_none() {
            return Ok(NativeGlassStatus::Unsupported {
                reason: "Native glass tint only applies to NSGlassEffectView.",
            });
        }
        let Some(glass_view) = content_view.downcast_ref::<NSGlassEffectView>() else {
            return Ok(NativeGlassStatus::Unsupported {
                reason: "Native glass tint only applies to NSGlassEffectView.",
            });
        };
        glass_view.setTintColor(tint.map(native_tint_color).as_deref());
        Ok(NativeGlassStatus::Applied {
            effect: LIQUID_GLASS_EFFECT_NAME,
        })
    }

    fn apply_liquid_glass(
        window: &NSWindow,
        content_view: &objc2_app_kit::NSView,
        main_thread: objc2::MainThreadMarker,
    ) -> NativeGlassStatus {
        let glass_view = NSGlassEffectView::new(main_thread);
        let autoresizing_mask = NSAutoresizingMaskOptions::ViewWidthSizable
            | NSAutoresizingMaskOptions::ViewHeightSizable;

        prepare_window_for_native_blur(window);
        glass_view.setFrame(content_view.frame());
        glass_view.setAutoresizingMask(autoresizing_mask);
        glass_view.setStyle(NSGlassEffectViewStyle::Regular);
        glass_view.setTintColor(Some(&NSColor::clearColor()));
        glass_view.setCornerRadius(ISLAND_BASE_CORNER_RADIUS);
        window.setContentView(Some(&glass_view));
        content_view.setFrame(glass_view.bounds());
        content_view.setAutoresizingMask(autoresizing_mask);
        glass_view.setContentView(Some(content_view));

        NativeGlassStatus::Applied {
            effect: LIQUID_GLASS_EFFECT_NAME,
        }
    }

    fn native_tint_color(tint: NativeGlassTint) -> objc2::rc::Retained<NSColor> {
        NSColor::colorWithDeviceRed_green_blue_alpha(
            clamp_unit(tint.red),
            clamp_unit(tint.green),
            clamp_unit(tint.blue),
            clamp_unit(tint.alpha),
        )
    }

    fn clamp_unit(value: f64) -> f64 {
        value.clamp(0.0, 1.0)
    }

    fn apply_visual_effect(
        window: &NSWindow,
        content_view: &objc2_app_kit::NSView,
        main_thread: objc2::MainThreadMarker,
    ) -> NativeGlassStatus {
        let effect_view = NSVisualEffectView::new(main_thread);
        let autoresizing_mask = NSAutoresizingMaskOptions::ViewWidthSizable
            | NSAutoresizingMaskOptions::ViewHeightSizable;

        prepare_window_for_native_blur(window);
        effect_view.setFrame(content_view.frame());
        effect_view.setAutoresizingMask(autoresizing_mask);
        effect_view.setMaterial(NSVisualEffectMaterial::UnderWindowBackground);
        effect_view.setBlendingMode(NSVisualEffectBlendingMode::BehindWindow);
        effect_view.setState(NSVisualEffectState::Active);
        effect_view.setEmphasized(false);
        window.setContentView(Some(&effect_view));
        content_view.setFrame(effect_view.bounds());
        content_view.setAutoresizingMask(autoresizing_mask);
        effect_view.addSubview(content_view);

        NativeGlassStatus::Applied {
            effect: VISUAL_EFFECT_NAME,
        }
    }

    fn prepare_window_for_native_blur(window: &NSWindow) {
        window.setOpaque(false);
        window.setBackgroundColor(Some(&NSColor::clearColor()));
    }

    fn select_native_effect(
        version: NSOperatingSystemVersion,
        liquid_glass_available: bool,
        visual_effect_available: bool,
    ) -> Option<NativeWindowEffect> {
        if is_macos_26_or_newer(version) && liquid_glass_available {
            return Some(NativeWindowEffect::LiquidGlass);
        }
        if visual_effect_available {
            return Some(NativeWindowEffect::VisualEffect);
        }
        None
    }

    fn is_macos_26_or_newer(version: NSOperatingSystemVersion) -> bool {
        version.majorVersion >= LIQUID_GLASS_MAJOR_VERSION
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        #[test]
        fn liquid_glass_requires_macos_26() {
            assert!(!is_macos_26_or_newer(NSOperatingSystemVersion {
                majorVersion: 25,
                minorVersion: 9,
                patchVersion: 9,
            }));
            assert!(is_macos_26_or_newer(NSOperatingSystemVersion {
                majorVersion: 26,
                minorVersion: 0,
                patchVersion: 0,
            }));
        }

        #[test]
        fn older_macos_uses_visual_effect_fallback() {
            assert_eq!(
                select_native_effect(
                    NSOperatingSystemVersion {
                        majorVersion: 25,
                        minorVersion: 9,
                        patchVersion: 9,
                    },
                    true,
                    true,
                ),
                Some(NativeWindowEffect::VisualEffect),
            );
        }

        #[test]
        fn macos_26_prefers_liquid_glass() {
            assert_eq!(
                select_native_effect(
                    NSOperatingSystemVersion {
                        majorVersion: 26,
                        minorVersion: 0,
                        patchVersion: 0,
                    },
                    true,
                    true,
                ),
                Some(NativeWindowEffect::LiquidGlass),
            );
        }

        #[test]
        fn clamps_tint_channels() {
            assert_eq!(clamp_unit(-1.0), 0.0);
            assert_eq!(clamp_unit(0.5), 0.5);
            assert_eq!(clamp_unit(2.0), 1.0);
        }
    }
}

#[cfg(windows)]
mod platform {
    use tauri::window::{Color, Effect, EffectsBuilder};
    use tauri::{Manager, Runtime, WebviewWindow};

    const ACRYLIC_EFFECT_NAME: &str = "windowEffect.acrylic";

    #[derive(serde::Serialize)]
    #[serde(rename_all = "camelCase", tag = "status")]
    pub enum NativeGlassStatus {
        Applied { effect: &'static str },
    }

    #[derive(Clone, Copy, serde::Deserialize)]
    #[serde(rename_all = "camelCase")]
    pub struct NativeGlassTint {
        red: f64,
        green: f64,
        blue: f64,
        alpha: f64,
    }

    pub async fn apply_to_label<R: Runtime>(
        app: tauri::AppHandle<R>,
        label: String,
    ) -> Result<NativeGlassStatus, String> {
        let window = app
            .get_webview_window(&label)
            .ok_or_else(|| format!("Window '{label}' was not found."))?;
        apply_acrylic(&window, None)
    }

    pub async fn set_tint_for_label<R: Runtime>(
        app: tauri::AppHandle<R>,
        label: String,
        tint: Option<NativeGlassTint>,
    ) -> Result<NativeGlassStatus, String> {
        let window = app
            .get_webview_window(&label)
            .ok_or_else(|| format!("Window '{label}' was not found."))?;
        apply_acrylic(&window, tint)
    }

    pub fn apply_to_window_now<R: Runtime>(
        window: &WebviewWindow<R>,
    ) -> Result<NativeGlassStatus, String> {
        apply_acrylic(window, None)
    }

    fn apply_acrylic<R: Runtime>(
        window: &WebviewWindow<R>,
        tint: Option<NativeGlassTint>,
    ) -> Result<NativeGlassStatus, String> {
        let mut effects = EffectsBuilder::new().effect(Effect::Acrylic);
        if let Some(tint) = tint {
            effects = effects.color(native_acrylic_color(tint));
        }
        window
            .set_effects(effects.build())
            .map_err(|error| format!("Apply Windows acrylic failed: {error}"))?;
        Ok(NativeGlassStatus::Applied {
            effect: ACRYLIC_EFFECT_NAME,
        })
    }

    fn native_acrylic_color(tint: NativeGlassTint) -> Color {
        Color(
            channel_to_u8(tint.red),
            channel_to_u8(tint.green),
            channel_to_u8(tint.blue),
            channel_to_u8(tint.alpha),
        )
    }

    fn channel_to_u8(value: f64) -> u8 {
        (value.clamp(0.0, 1.0) * 255.0).round() as u8
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        #[test]
        fn maps_tint_channels_to_bytes() {
            assert_eq!(channel_to_u8(-1.0), 0);
            assert_eq!(channel_to_u8(0.0), 0);
            assert_eq!(channel_to_u8(1.0), 255);
            assert_eq!(channel_to_u8(2.0), 255);
        }
    }
}

#[cfg(not(any(target_os = "macos", windows)))]
mod platform {
    use tauri::Runtime;

    #[derive(serde::Serialize)]
    #[serde(rename_all = "camelCase", tag = "status")]
    pub enum NativeGlassStatus {
        Unsupported { reason: &'static str },
    }

    #[derive(Clone, Copy, serde::Deserialize)]
    #[serde(rename_all = "camelCase")]
    pub struct NativeGlassTint {
        #[allow(dead_code)]
        red: f64,
        #[allow(dead_code)]
        green: f64,
        #[allow(dead_code)]
        blue: f64,
        #[allow(dead_code)]
        alpha: f64,
    }

    pub async fn apply_to_label<R: Runtime>(
        _app: tauri::AppHandle<R>,
        _label: String,
    ) -> Result<NativeGlassStatus, String> {
        Ok(NativeGlassStatus::Unsupported {
            reason: "Native window blur is unavailable on this platform.",
        })
    }

    pub async fn set_tint_for_label<R: Runtime>(
        _app: tauri::AppHandle<R>,
        _label: String,
        _tint: Option<NativeGlassTint>,
    ) -> Result<NativeGlassStatus, String> {
        Ok(NativeGlassStatus::Unsupported {
            reason: "Native window blur is unavailable on this platform.",
        })
    }
}

#[cfg(any(target_os = "macos", windows))]
pub use platform::apply_to_window_now;
pub use platform::{apply_to_label, set_tint_for_label, NativeGlassStatus, NativeGlassTint};
