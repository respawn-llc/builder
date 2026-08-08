import boundaries from "eslint-plugin-boundaries";
import tseslint from "typescript-eslint";

export const architectureOwners = Object.freeze({
  API: "api",
  APP_FACADE: "app-facade",
  FEATURE: "feature",
  I18N: "i18n",
  NATIVE_CONFIG: "native-config",
  NATIVE_PACKAGE: "native-package",
  SHARED: "shared",
  SHELL: "shell",
  TEST_SUPPORT: "test-support",
  TOOLING: "tooling",
  UI: "ui",
  UI_KIT: "ui-kit",
  VENDOR: "vendor",
});

export const architectureElements = Object.freeze([
  architectureElement(architectureOwners.SHELL, "src/app"),
  architectureElement(architectureOwners.SHELL, "src/dev-showcase"),
  architectureElement(architectureOwners.APP_FACADE, "src/app-facade"),
  architectureElement(architectureOwners.FEATURE, "src/features/*", "name"),
  architectureElement(architectureOwners.SHARED, "src/shared/*", "name"),
  architectureElement(architectureOwners.UI, "src/ui"),
  architectureElement(architectureOwners.API, "src/api"),
  architectureElement(architectureOwners.TEST_SUPPORT, "src/test-support"),
  architectureElement(architectureOwners.UI_KIT, "packages/ui-kit"),
  architectureElement(architectureOwners.NATIVE_PACKAGE, "packages/*", "name"),
  architectureElement(architectureOwners.I18N, "src/i18n"),
  architectureElement(architectureOwners.VENDOR, "src/vendor"),
  architectureElement(architectureOwners.NATIVE_CONFIG, "src-tauri"),
  architectureElement(architectureOwners.TOOLING, "tooling"),
  architectureElement(architectureOwners.TOOLING, "test"),
  architectureElement(architectureOwners.TOOLING, "src/types"),
]);

export const architectureEntrypoints = Object.freeze({
  API_COMPOSITION: "composition/index.ts",
  INDEX: "index.ts",
  NATIVE_PACKAGE: "src/index.ts",
  UI_KIT: "src/ReorderableList.tsx",
  VENDOR_ELK_API: "elkjs-types.ts",
  VENDOR_ELK_BUNDLED: "elkjs-bundled-types.ts",
  VENDOR_XYFLOW: "xyflow-react-types.ts",
});

export const architectureFileCategories = Object.freeze({
  DECLARATION: "declaration",
  TEST: "test",
});

export const architectureFiles = Object.freeze([
  architectureFile(architectureFileCategories.TEST, "**/*.test.{ts,tsx}"),
  architectureFile(architectureFileCategories.TEST, "test/**/*.{ts,tsx}"),
  architectureFile(architectureFileCategories.DECLARATION, "**/*.d.ts"),
]);

export const architectureDependencyNodes = Object.freeze(["import", "export", "dynamic-import", "require"]);

export const architectureAdditionalDependencyNodes = Object.freeze(
  ["mock", "doMock", "unmock", "doUnmock", "importActual", "importMock"].map((method) =>
    Object.freeze({
      selector: `CallExpression[callee.object.name=vi][callee.property.name=${method}] > Literal.arguments:first-child`,
      kind: "value",
      name: `vi.${method}`,
    }),
  ),
);

const dependencyTargets = {
  API: dependencyTarget(architectureOwners.API, "@/api"),
  API_COMPOSITION: dependencyTarget(
    architectureOwners.API,
    "@/api/composition",
    architectureEntrypoints.API_COMPOSITION,
  ),
  APP_FACADE: dependencyTarget(architectureOwners.APP_FACADE, "@/app-facade"),
  FEATURE: dependencyTarget(architectureOwners.FEATURE, "@/features/*"),
  I18N: dependencyTarget(architectureOwners.I18N, "@/i18n"),
  NATIVE_PACKAGE: dependencyTarget(
    architectureOwners.NATIVE_PACKAGE,
    "@app/native-bridge",
    architectureEntrypoints.NATIVE_PACKAGE,
  ),
  SHARED: dependencyTarget(architectureOwners.SHARED, "@/shared/*"),
  TOOLING_TYPES: dependencyTarget(architectureOwners.TOOLING, "@/types"),
  UI: dependencyTarget(architectureOwners.UI, "@/ui"),
  UI_KIT: dependencyTarget(
    architectureOwners.UI_KIT,
    "@app/ui-kit",
    architectureEntrypoints.UI_KIT,
  ),
  VENDOR_ELK_API: dependencyTarget(
    architectureOwners.VENDOR,
    "elkjs/lib/elk-api",
    architectureEntrypoints.VENDOR_ELK_API,
  ),
  VENDOR_ELK_BUNDLED: dependencyTarget(
    architectureOwners.VENDOR,
    "elkjs/lib/elk.bundled.js",
    architectureEntrypoints.VENDOR_ELK_BUNDLED,
  ),
  VENDOR_XYFLOW: dependencyTarget(
    architectureOwners.VENDOR,
    "@xyflow/react",
    architectureEntrypoints.VENDOR_XYFLOW,
  ),
};

const vendorDependencies = [
  dependencyTargets.VENDOR_XYFLOW,
  dependencyTargets.VENDOR_ELK_API,
  dependencyTargets.VENDOR_ELK_BUNDLED,
];

const compositionDependencies = [
  dependencyTargets.APP_FACADE,
  dependencyTargets.FEATURE,
  dependencyTargets.SHARED,
  dependencyTargets.UI,
  dependencyTargets.API,
  dependencyTargets.API_COMPOSITION,
  dependencyTargets.I18N,
  dependencyTargets.NATIVE_PACKAGE,
  ...vendorDependencies,
];

const ownerDependencyMatrix = [
  ownerDependencies(
    architectureOwners.SHELL,
    dependencyTarget(architectureOwners.SHELL),
    ...compositionDependencies,
  ),
  ownerDependencies(
    architectureOwners.APP_FACADE,
    dependencyTargets.API,
    dependencyTargets.UI,
    dependencyTargets.NATIVE_PACKAGE,
  ),
  ownerDependencies(
    architectureOwners.FEATURE,
    dependencyTargets.APP_FACADE,
    dependencyTargets.SHARED,
    dependencyTargets.UI,
    dependencyTargets.UI_KIT,
    dependencyTargets.API,
    dependencyTargets.I18N,
    ...vendorDependencies,
  ),
  ownerDependencies(
    architectureOwners.SHARED,
    dependencyTargets.APP_FACADE,
    dependencyTargets.API,
    dependencyTargets.UI,
    dependencyTargets.UI_KIT,
    dependencyTargets.SHARED,
    ...vendorDependencies,
  ),
  ownerDependencies(architectureOwners.UI_KIT, dependencyTargets.UI_KIT),
  ownerDependencies(architectureOwners.TEST_SUPPORT, ...compositionDependencies),
  ownerDependencies(architectureOwners.TOOLING, dependencyTargets.TOOLING_TYPES),
];

const ownerDependencyPolicies = ownerDependencyMatrix.flatMap(({ from, dependencies }) =>
  dependencies.map((dependency) => allowOwnerDependency({ from, ...dependency })),
);

const testDependencyPolicies = Object.freeze([
  Object.freeze({
    from: {
      file: {
        categories: architectureFileCategories.TEST,
      },
    },
    allow: {
      to: {
        element: {
          types: architectureOwners.TEST_SUPPORT,
          fileInternalPath: "*/index.ts",
        },
      },
      dependency: {
        source: "@/test-support/*",
      },
    },
  }),
  Object.freeze({
    from: {
      file: {
        categories: architectureFileCategories.TEST,
      },
    },
    allow: {
      to: {
        element: {
          types: architectureOwners.NATIVE_CONFIG,
          fileInternalPath: ["tauri.conf.json", "capabilities/default.json"],
        },
      },
      dependency: {
        source: ["../src-tauri/**", "../../src-tauri/**"],
      },
    },
  }),
]);

export const architectureDependencyOptions = Object.freeze({
  default: "disallow",
  checkUnknownLocals: true,
  policies: Object.freeze([...ownerDependencyPolicies, ...testDependencyPolicies]),
});

export function createArchitecturePolicy({ rootPath, parserProjects }) {
  return [
    {
      files: ["**/*.{ts,tsx}"],
      languageOptions: {
        parser: tseslint.parser,
        parserOptions: {
          project: parserProjects,
          tsconfigRootDir: rootPath,
        },
      },
      plugins: {
        boundaries,
      },
      settings: {
        "boundaries/elements": architectureElements,
        "boundaries/elements-single-type": true,
        "boundaries/files": architectureFiles,
        "boundaries/root-path": rootPath,
        "boundaries/dependency-nodes": architectureDependencyNodes,
        "boundaries/additional-dependency-nodes": architectureAdditionalDependencyNodes,
        "import/resolver": {
          typescript: {
            extensions: [".css", ".js", ".jsx", ".json", ".ts", ".tsx"],
            project: parserProjects,
          },
        },
      },
      rules: {
        "boundaries/dependencies": ["error", architectureDependencyOptions],
        "boundaries/no-unknown-dependencies": ["error", { require: "element" }],
        "boundaries/no-unknown-files": "error",
      },
    },
    {
      files: ["**/*.{ts,tsx}"],
      ignores: ["packages/native-bridge/**"],
      rules: {
        "no-restricted-imports": [
          "error",
          {
            paths: [
              {
                name: "@tauri-apps/api",
                message: "Import Tauri APIs only inside the native bridge package.",
              },
            ],
            patterns: [
              {
                group: ["@tauri-apps/api/*", "@tauri-apps/plugin-*"],
                message: "Import Tauri APIs only inside the native bridge package.",
              },
            ],
          },
        ],
      },
    },
  ];
}

function allowOwnerDependency({ from, to, source, targetFile = architectureEntrypoints.INDEX }) {
  const dependencySelector = {
    to: {
      element: {
        types: to,
        fileInternalPath: targetFile,
      },
    },
  };
  if (source !== undefined) {
    dependencySelector.dependency = { source };
  }
  return Object.freeze({
    from: {
      element: {
        types: from,
      },
    },
    allow: dependencySelector,
  });
}

function dependencyTarget(to, source, targetFile) {
  return Object.freeze({ to, source, targetFile });
}

function ownerDependencies(from, ...dependencies) {
  return { from, dependencies };
}

function architectureElement(type, pattern, capture) {
  const element = {
    type,
    pattern,
    partialMatch: false,
  };
  if (capture !== undefined) {
    element.capture = Object.freeze([capture]);
  }
  return Object.freeze(element);
}

function architectureFile(category, pattern) {
  return Object.freeze({ category, pattern });
}
