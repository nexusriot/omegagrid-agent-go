from __future__ import annotations

import os
from typing import Any, Dict

import requests

from skills.base import BaseSkill

_TIMEOUT = float(os.environ.get("SKILL_HTTP_TIMEOUT", "30"))


class WeatherSkill(BaseSkill):
    """Fetches current weather for a given city using the free Open-Meteo API (no API key needed)."""

    name = "weather"
    description = "Get current weather (temperature, wind, humidity) for a city. No API key required."
    parameters = {
        "city": {"type": "string", "description": "City name, e.g. 'London'", "required": True},
    }

    def execute(self, city: str, **kwargs) -> Dict[str, Any]:
        try:
            geo = requests.get(
                "https://geocoding-api.open-meteo.com/v1/search",
                params={"name": city, "count": 1, "language": "en"},
                timeout=_TIMEOUT,
            )
            geo.raise_for_status()
        except requests.exceptions.Timeout:
            return {"error": f"Geocoding timed out after {_TIMEOUT}s — try again or increase SKILL_HTTP_TIMEOUT"}
        except requests.exceptions.RequestException as exc:
            return {"error": f"Geocoding request failed: {exc}"}

        results = geo.json().get("results")
        if not results:
            return {"error": f"City '{city}' not found"}

        loc = results[0]
        lat, lon = loc["latitude"], loc["longitude"]
        resolved_name = loc.get("name", city)
        country = loc.get("country", "")

        try:
            weather = requests.get(
                "https://api.open-meteo.com/v1/forecast",
                params={
                    "latitude": lat,
                    "longitude": lon,
                    "current_weather": True,
                    "hourly": "relative_humidity_2m",
                    "timezone": "auto",
                },
                timeout=_TIMEOUT,
            )
            weather.raise_for_status()
        except requests.exceptions.Timeout:
            return {"error": f"Weather API timed out after {_TIMEOUT}s — try again or increase SKILL_HTTP_TIMEOUT"}
        except requests.exceptions.RequestException as exc:
            return {"error": f"Weather request failed: {exc}"}

        data = weather.json()
        cw = data.get("current_weather", {})

        humidity_values = data.get("hourly", {}).get("relative_humidity_2m", [])
        humidity = humidity_values[0] if humidity_values else None

        return {
            "city": resolved_name,
            "country": country,
            "latitude": lat,
            "longitude": lon,
            "temperature_c": cw.get("temperature"),
            "windspeed_kmh": cw.get("windspeed"),
            "wind_direction_deg": cw.get("winddirection"),
            "weather_code": cw.get("weathercode"),
            "humidity_percent": humidity,
            "time": cw.get("time"),
        }
