defmodule Fixture.MixProject do
  use Mix.Project

  def project do
    [
      app: :fixture,
      version: "0.1.0",
      deps: deps()
    ]
  end

  defp deps do
    [
      {:jason, "~> 1.4"},
      {:nimble_csv, "~> 1.2"}
    ]
  end
end
