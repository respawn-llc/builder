import * as astro from "prettier-plugin-astro";

export default {
  plugins: [astro],
  overrides: [
    {
      files: "*.astro",
      options: { parser: "astro" },
    },
  ],
};
