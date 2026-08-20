import { defineCollection } from "astro:content";
import { docsSchema } from "@astrojs/starlight/schema";

import { createDocsCollectionLoader } from "../scripts/docs-collection-loader.mjs";

export const collections = {
  docs: defineCollection({
    loader: createDocsCollectionLoader(),
    schema: docsSchema(),
  }),
};
