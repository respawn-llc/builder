use serde::{
    Deserialize, Deserializer, Serialize, Serializer, de::Error as _, ser::Error as _,
    ser::SerializeStruct,
};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TranscriptDiagnostic {
    pub code: String,
    pub detail: String,
    pub developer: Option<DeveloperDiagnostic>,
}

impl TranscriptDiagnostic {
    fn validate(&self) -> Result<(), &'static str> {
        let has_legacy = !self.code.trim().is_empty() || !self.detail.trim().is_empty();
        let has_developer = self.developer.is_some();
        if has_legacy == has_developer {
            return Err(
                "transcript diagnostic must contain exactly one legacy or developer variant",
            );
        }
        if has_legacy && (self.code.trim().is_empty() || self.detail.trim().is_empty()) {
            return Err("transcript diagnostic legacy code and detail are both required");
        }
        if let Some(developer) = &self.developer {
            developer.validate()?;
        }
        Ok(())
    }
}

impl Serialize for TranscriptDiagnostic {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        self.validate().map_err(S::Error::custom)?;
        let mut state = serializer.serialize_struct("TranscriptDiagnostic", 3)?;
        state.serialize_field("Code", &self.code)?;
        state.serialize_field("Detail", &self.detail)?;
        state.serialize_field("Developer", &self.developer)?;
        state.end()
    }
}

impl<'de> Deserialize<'de> for TranscriptDiagnostic {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        #[derive(Deserialize)]
        struct WireDiagnostic {
            #[serde(rename = "Code")]
            code: String,
            #[serde(rename = "Detail")]
            detail: String,
            #[serde(rename = "Developer", default)]
            developer: Option<DeveloperDiagnostic>,
        }

        let wire = WireDiagnostic::deserialize(deserializer)?;
        let diagnostic = Self {
            code: wire.code,
            detail: wire.detail,
            developer: wire.developer,
        };
        diagnostic.validate().map_err(D::Error::custom)?;
        Ok(diagnostic)
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DeveloperDiagnostic {
    pub deletion_fact_mismatch: Option<DeletionFactMismatchDeveloperDiagnostic>,
}

impl DeveloperDiagnostic {
    fn validate(&self) -> Result<(), &'static str> {
        let mismatch = self
            .deletion_fact_mismatch
            .as_ref()
            .ok_or("developer diagnostic variant is required")?;
        mismatch.validate()
    }
}

impl Serialize for DeveloperDiagnostic {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        self.validate().map_err(S::Error::custom)?;
        let mut state = serializer.serialize_struct("DeveloperDiagnostic", 1)?;
        state.serialize_field("deletion_fact_mismatch", &self.deletion_fact_mismatch)?;
        state.end()
    }
}

impl<'de> Deserialize<'de> for DeveloperDiagnostic {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        #[derive(Deserialize)]
        struct WireDeveloperDiagnostic {
            #[serde(rename = "deletion_fact_mismatch", default)]
            deletion_fact_mismatch: Option<DeletionFactMismatchDeveloperDiagnostic>,
        }

        let wire = WireDeveloperDiagnostic::deserialize(deserializer)?;
        let diagnostic = Self {
            deletion_fact_mismatch: wire.deletion_fact_mismatch,
        };
        diagnostic.validate().map_err(D::Error::custom)?;
        Ok(diagnostic)
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DeletionFactMismatchDeveloperDiagnostic {
    pub call_id: String,
    pub operation_id: WholeFileDeletionOperationId,
    pub mismatch_kind: DeletionFactMismatchKind,
}

impl DeletionFactMismatchDeveloperDiagnostic {
    fn validate(&self) -> Result<(), &'static str> {
        if self.call_id.trim().is_empty() {
            return Err("deletion fact mismatch diagnostic call id is required");
        }
        if self.operation_id.hunk_ordinal < 0 {
            return Err("deletion fact mismatch diagnostic hunk ordinal must be non-negative");
        }
        Ok(())
    }
}

impl Serialize for DeletionFactMismatchDeveloperDiagnostic {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        self.validate().map_err(S::Error::custom)?;
        let mut state =
            serializer.serialize_struct("DeletionFactMismatchDeveloperDiagnostic", 3)?;
        state.serialize_field("call_id", &self.call_id)?;
        state.serialize_field("operation_id", &self.operation_id)?;
        state.serialize_field("mismatch_kind", &self.mismatch_kind)?;
        state.end()
    }
}

impl<'de> Deserialize<'de> for DeletionFactMismatchDeveloperDiagnostic {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        #[derive(Deserialize)]
        struct WireMismatch {
            call_id: String,
            operation_id: WholeFileDeletionOperationId,
            mismatch_kind: DeletionFactMismatchKind,
        }

        let wire = WireMismatch::deserialize(deserializer)?;
        let mismatch = Self {
            call_id: wire.call_id,
            operation_id: wire.operation_id,
            mismatch_kind: wire.mismatch_kind,
        };
        mismatch.validate().map_err(D::Error::custom)?;
        Ok(mismatch)
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WholeFileDeletionOperationId {
    #[serde(rename = "HunkOrdinal")]
    pub hunk_ordinal: i32,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
pub enum DeletionFactMismatchKind {
    #[serde(rename = "duplicate")]
    Duplicate,
    #[serde(rename = "unmatched")]
    Unmatched,
    #[serde(rename = "missing")]
    Missing,
    #[serde(rename = "invalid_count")]
    InvalidCount,
}
