use serde::{
    Deserialize, Deserializer, Serialize, Serializer, de::Error as _, ser::Error as _,
    ser::SerializeStruct,
};

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TranscriptDiagnostic {
    Legacy { code: String, detail: String },
    Developer(DeveloperDiagnostic),
}

impl Serialize for TranscriptDiagnostic {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        let mut state = serializer.serialize_struct("TranscriptDiagnostic", 3)?;
        match self {
            Self::Legacy { code, detail } => {
                validate_legacy(code, detail).map_err(S::Error::custom)?;
                state.serialize_field("Code", &Some(code))?;
                state.serialize_field("Detail", &Some(detail))?;
                state.serialize_field("Developer", &Option::<&DeveloperDiagnostic>::None)?;
            }
            Self::Developer(developer) => {
                state.serialize_field("Code", &Option::<&String>::None)?;
                state.serialize_field("Detail", &Option::<&String>::None)?;
                state.serialize_field("Developer", &Some(developer))?;
            }
        }
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
            code: Option<String>,
            #[serde(rename = "Detail")]
            detail: Option<String>,
            #[serde(rename = "Developer", default)]
            developer: Option<DeveloperDiagnostic>,
        }

        let wire = WireDiagnostic::deserialize(deserializer)?;
        match (wire.code, wire.detail, wire.developer) {
            (Some(code), Some(detail), None) => {
                validate_legacy(&code, &detail).map_err(D::Error::custom)?;
                Ok(Self::Legacy { code, detail })
            }
            (None, None, Some(developer)) => Ok(Self::Developer(developer)),
            _ => Err(D::Error::custom(
                "transcript diagnostic must contain exactly one complete legacy or developer variant",
            )),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum DeveloperDiagnostic {
    DeletionFactMismatch(DeletionFactMismatchDeveloperDiagnostic),
}

impl Serialize for DeveloperDiagnostic {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        let mut state = serializer.serialize_struct("DeveloperDiagnostic", 1)?;
        match self {
            Self::DeletionFactMismatch(mismatch) => {
                mismatch.validate().map_err(S::Error::custom)?;
                state.serialize_field("deletion_fact_mismatch", mismatch)?;
            }
        }
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
        let mismatch = wire
            .deletion_fact_mismatch
            .ok_or_else(|| D::Error::custom("developer diagnostic variant is required"))?;
        Ok(Self::DeletionFactMismatch(mismatch))
    }
}

fn validate_legacy(code: &str, detail: &str) -> Result<(), &'static str> {
    if code.trim().is_empty() || detail.trim().is_empty() {
        return Err("transcript diagnostic legacy code and detail are both required");
    }
    Ok(())
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
