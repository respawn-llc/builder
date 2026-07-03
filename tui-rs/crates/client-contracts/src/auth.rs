use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize, Serialize)]
pub struct AuthBootstrapOAuthConfig {
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub issuer: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub client_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct AuthGetBootstrapStatusResponse {
    pub auth_ready: bool,
    pub auth_required: bool,
    pub auth_bootstrap_supported: bool,
    #[serde(default)]
    pub allowed_pre_auth_methods: Vec<String>,
    #[serde(default)]
    pub supported_modes: Vec<AuthBootstrapMode>,
    #[serde(default)]
    pub oauth: AuthBootstrapOAuthConfig,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize, Serialize)]
pub struct AuthStatusRequest {}

#[derive(Debug, Clone, Default, PartialEq, Deserialize, Serialize)]
pub struct AuthStatusResponse {
    #[serde(default)]
    pub auth: AuthStatusInfo,
    #[serde(default)]
    pub subscription: AuthSubscriptionInfo,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub warning: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize, Serialize)]
pub struct AuthStatusInfo {
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub summary: String,
    #[serde(skip_serializing_if = "Vec::is_empty", default)]
    pub details: Vec<String>,
    #[serde(skip_serializing_if = "is_false", default)]
    pub visible: bool,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub method: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub provider: String,
    #[serde(skip_serializing_if = "is_false", default)]
    pub unavailable: bool,
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize, Serialize)]
pub struct AuthSubscriptionInfo {
    #[serde(skip_serializing_if = "is_false", default)]
    pub applicable: bool,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub summary: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub error: String,
    #[serde(skip_serializing_if = "Vec::is_empty", default)]
    pub windows: Vec<AuthSubscriptionWindow>,
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize, Serialize)]
pub struct AuthSubscriptionWindow {
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub label: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub qualifier: String,
    #[serde(skip_serializing_if = "is_zero_f64", default)]
    pub used_percent: f64,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub reset_at: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct AuthCompleteBootstrapRequest {
    pub mode: AuthBootstrapMode,
    #[serde(skip_serializing_if = "is_false", default)]
    pub force: bool,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub api_key: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub callback_input: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub redirect_uri: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub oauth_state: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub oauth_code_verifier: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub device_authorization_code: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub device_code_verifier: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct AuthCompleteBootstrapResponse {
    pub auth_ready: bool,
    #[serde(default)]
    pub method_type: String,
    #[serde(default)]
    pub account_id: String,
    #[serde(default)]
    pub email: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AuthBootstrapMode {
    None,
    BrowserCallbackUrl,
    BrowserCallbackCode,
    DeviceCode,
    ApiKey,
    Unknown(String),
}

impl Serialize for AuthBootstrapMode {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        serializer.serialize_str(self.as_str())
    }
}

impl<'de> Deserialize<'de> for AuthBootstrapMode {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let value = String::deserialize(deserializer)?;
        Ok(Self::from_wire(value))
    }
}

impl AuthBootstrapMode {
    fn as_str(&self) -> &str {
        match self {
            Self::None => "none",
            Self::BrowserCallbackUrl => "browser_callback_url",
            Self::BrowserCallbackCode => "browser_callback_code",
            Self::DeviceCode => "device_code",
            Self::ApiKey => "api_key",
            Self::Unknown(value) => value,
        }
    }

    fn from_wire(value: String) -> Self {
        match value.as_str() {
            "none" => Self::None,
            "browser_callback_url" => Self::BrowserCallbackUrl,
            "browser_callback_code" => Self::BrowserCallbackCode,
            "device_code" => Self::DeviceCode,
            "api_key" => Self::ApiKey,
            _ => Self::Unknown(value),
        }
    }
}

fn is_false(value: &bool) -> bool {
    !*value
}

fn is_zero_f64(value: &f64) -> bool {
    *value == 0.0
}
